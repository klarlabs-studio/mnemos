package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/felixgeelhaar/bolt"
	mcp "github.com/felixgeelhaar/mcp-go"
	"github.com/felixgeelhaar/mnemos/internal/autoedge"
	"github.com/felixgeelhaar/mnemos/internal/domain"
	"github.com/felixgeelhaar/mnemos/internal/embedding"
	"github.com/felixgeelhaar/mnemos/internal/ingest"
	"github.com/felixgeelhaar/mnemos/internal/llm"
	"github.com/felixgeelhaar/mnemos/internal/parser"
	"github.com/felixgeelhaar/mnemos/internal/pipeline"
	"github.com/felixgeelhaar/mnemos/internal/ports"
	"github.com/felixgeelhaar/mnemos/internal/query"
	"github.com/felixgeelhaar/mnemos/internal/relate"
	"github.com/felixgeelhaar/mnemos/internal/store"
	"github.com/felixgeelhaar/mnemos/internal/synthesize"
	"github.com/felixgeelhaar/mnemos/internal/trust"
	"github.com/felixgeelhaar/mnemos/internal/workflow"
)

type mcpBoltLogger struct {
	logger *bolt.Logger
}

func (l mcpBoltLogger) Info(msg string, fields ...mcp.LogField) {
	l.log(l.logger.Info(), msg, fields...)
}
func (l mcpBoltLogger) Error(msg string, fields ...mcp.LogField) {
	l.log(l.logger.Error(), msg, fields...)
}
func (l mcpBoltLogger) Debug(msg string, fields ...mcp.LogField) {
	l.log(l.logger.Debug(), msg, fields...)
}
func (l mcpBoltLogger) Warn(msg string, fields ...mcp.LogField) {
	l.log(l.logger.Warn(), msg, fields...)
}

func (l mcpBoltLogger) log(event *bolt.Event, msg string, fields ...mcp.LogField) {
	for _, field := range fields {
		event = event.Any(field.Key, field.Value)
	}
	event.Msg(msg)
}

type mcpQueryInput struct {
	Question string `json:"question" jsonschema:"required,description=Natural language question to ask Mnemos"`
	RunID    string `json:"runId,omitempty" jsonschema:"description=Optional run ID to scope the query"`
	Hops     int    `json:"hops,omitempty" jsonschema:"description=BFS hop expansion depth through supports/contradicts edges (0-5, default 0)"`
}

type mcpQueryOutput struct {
	Answer         string                `json:"answer"`
	Claims         []domain.Claim        `json:"claims"`
	Contradictions []domain.Relationship `json:"contradictions"`
	Timeline       []string              `json:"timeline"`
	// ClaimProvenance maps claim ID to "local" or a registry URL so the
	// agent can show which claims came from a federated registry.
	ClaimProvenance map[string]string `json:"claim_provenance,omitempty"`
	// ClaimHopDistance maps claim ID to the BFS hop count from the
	// directly-retrieved set (0 = direct, N>0 = expanded via N hops of
	// supports/contradicts). Empty when hops=0.
	ClaimHopDistance map[string]int `json:"claim_hop_distance,omitempty"`
}

type mcpProcessTextInput struct {
	Text          string `json:"text" jsonschema:"required,description=Raw text to ingest and process"`
	UseLLM        bool   `json:"useLlm,omitempty" jsonschema:"description=Use configured LLM extraction provider"`
	UseEmbeddings bool   `json:"useEmbeddings,omitempty" jsonschema:"description=Generate embeddings after processing"`
}

type mcpProcessTextOutput struct {
	RunID          string `json:"runId"`
	Events         int    `json:"events"`
	Claims         int    `json:"claims"`
	Relationships  int    `json:"relationships"`
	Embeddings     int    `json:"embeddings"`
	UsedLLM        bool   `json:"usedLlm"`
	UsedEmbeddings bool   `json:"usedEmbeddings"`
}

type mcpMetricsOutput struct {
	Runs            int64 `json:"runs"`
	Events          int64 `json:"events"`
	Claims          int64 `json:"claims"`
	ContestedClaims int64 `json:"contested_claims"`
	Relationships   int64 `json:"relationships"`
	Contradictions  int64 `json:"contradictions"`
	Embeddings      int64 `json:"embeddings"`
}

type mcpIngestGitLogInput struct {
	Limit int    `json:"limit,omitempty" jsonschema:"description=Max number of commits to ingest (default 50, cap 1000)"`
	Since string `json:"since,omitempty" jsonschema:"description=Optional date string passed to git --since (e.g. '2026-01-01' or '2 weeks ago')"`
}

type mcpIngestGitLogOutput struct {
	Ingested int `json:"ingested"`
	Skipped  int `json:"skipped"`
}

type mcpIngestGitPRsInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"description=Max number of merged PRs to ingest (default 20, cap 200)"`
}

type mcpIngestGitPRsOutput struct {
	Ingested int `json:"ingested"`
	Skipped  int `json:"skipped"`
}

type mcpWatchFileInput struct {
	Path string `json:"path" jsonschema:"required,description=Absolute or relative path to the file to watch for changes"`
}

type mcpWatchFileOutput struct {
	Watching      bool   `json:"watching"`
	Path          string `json:"path"`
	ActiveWatches int    `json:"activeWatches"`
}

type mcpRecordActionInput struct {
	Kind     string            `json:"kind" jsonschema:"required,description=Action kind (deploy, rollback, restart, scale, configure, migrate, feature_flag, hotfix, custom)"`
	Subject  string            `json:"subject" jsonschema:"required,description=Service or component the action targets"`
	Actor    string            `json:"actor,omitempty" jsonschema:"description=User or agent id that performed the action"`
	RunID    string            `json:"runId,omitempty" jsonschema:"description=Optional run id for scoped storage"`
	At       string            `json:"at,omitempty" jsonschema:"description=When the action happened (RFC3339 / YYYY-MM-DD / 'now', defaults to now)"`
	Metadata map[string]string `json:"metadata,omitempty" jsonschema:"description=Free-form string metadata for the action"`
}

type mcpRecordActionOutput struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
}

type mcpRecordOutcomeInput struct {
	ActionID   string             `json:"actionId" jsonschema:"required,description=Id of the action this outcome reports on"`
	Result     string             `json:"result" jsonschema:"required,description=success | failure | partial | unknown"`
	Metrics    map[string]float64 `json:"metrics,omitempty" jsonschema:"description=Numeric metric observations (e.g. latency_after_ms, error_rate)"`
	Notes      string             `json:"notes,omitempty" jsonschema:"description=Free-form human notes about the outcome"`
	ObservedAt string             `json:"observedAt,omitempty" jsonschema:"description=When the outcome was observed (RFC3339, defaults to now)"`
	Source     string             `json:"source,omitempty" jsonschema:"description=Source of the report (default 'push'; pull adapters use 'pull:<name>')"`
}

type mcpRecordOutcomeOutput struct {
	ID       string `json:"id"`
	ActionID string `json:"action_id"`
	Result   string `json:"result"`
}

type mcpQueryLessonsInput struct {
	Service string `json:"service,omitempty" jsonschema:"description=Filter to lessons scoped to this service"`
	Trigger string `json:"trigger,omitempty" jsonschema:"description=Filter to lessons matching this trigger label"`
}

type mcpWhichTestToTrustInput struct {
	RequirementRef string `json:"requirement_ref" jsonschema:"required,description=Test requirement reference; matches Claim.TestRequirementRef on test_result claims"`
	// Optional scope filter — narrows the candidate set to claims
	// whose Scope matches the supplied filter. An empty filter (the
	// zero value) returns every test_result for the requirement,
	// matching the previous behaviour. Use this from a low-privilege
	// caller to avoid enumerating tests across services / environments
	// / teams that are not in scope.
	Service string `json:"service,omitempty" jsonschema:"description=Restrict to claims whose Scope.Service matches"`
	Env     string `json:"env,omitempty" jsonschema:"description=Restrict to claims whose Scope.Env matches"`
	Team    string `json:"team,omitempty" jsonschema:"description=Restrict to claims whose Scope.Team matches"`
}

type mcpWhichTestToTrustCandidate struct {
	ClaimID        string  `json:"claim_id"`
	Text           string  `json:"text"`
	TestID         string  `json:"test_id,omitempty"`
	TestAuthor     string  `json:"test_author,omitempty"`
	TestLastRunAt  string  `json:"test_last_run_at,omitempty"`
	TestPassCount  int     `json:"test_pass_count"`
	TestFailCount  int     `json:"test_fail_count"`
	Score          float64 `json:"score"`
	Rationale      string  `json:"rationale"`       // compact metric breakdown
	ProseRationale string  `json:"prose_rationale"` // operator-readable
}

type mcpWhichTestToTrustOutput struct {
	RequirementRef string                         `json:"requirement_ref"`
	Verdict        string                         `json:"verdict"`
	WinnerClaimID  string                         `json:"winner_claim_id,omitempty"`
	WinnerScore    float64                        `json:"winner_score,omitempty"`
	Candidates     []mcpWhichTestToTrustCandidate `json:"candidates"`
}

type mcpQueryLessonsOutput struct {
	Lessons []domain.Lesson `json:"lessons"`
}

type mcpSynthesizeInput struct {
	MinCorroboration int     `json:"minCorroboration,omitempty" jsonschema:"description=Override the minimum cluster size before a lesson is emitted"`
	MinConfidence    float64 `json:"minConfidence,omitempty" jsonschema:"description=Override the minimum confidence floor in [0, 1]"`
}

type mcpSynthesizeOutput struct {
	Clusters       int      `json:"clusters"`
	LessonsEmitted int      `json:"lessons_emitted"`
	Skipped        int      `json:"skipped"`
	LessonIDs      []string `json:"lesson_ids"`
}

type mcpRecordDecisionInput struct {
	Statement    string   `json:"statement" jsonschema:"required,description=Short statement of the decision being recorded"`
	Plan         string   `json:"plan,omitempty" jsonschema:"description=Plan or chosen course of action"`
	Reasoning    string   `json:"reasoning,omitempty" jsonschema:"description=Free-form rationale for choosing this plan"`
	RiskLevel    string   `json:"riskLevel" jsonschema:"required,description=low | medium | high | critical"`
	Beliefs      []string `json:"beliefs,omitempty" jsonschema:"description=Claim ids that justified this decision"`
	Alternatives []string `json:"alternatives,omitempty" jsonschema:"description=Plans considered but not chosen"`
	OutcomeID    string   `json:"outcomeId,omitempty" jsonschema:"description=Optional Outcome id when the result is already known"`
	ChosenAt     string   `json:"chosenAt,omitempty" jsonschema:"description=When the decision was made (RFC3339 / 'now', defaults to now)"`
}

type mcpRecordDecisionOutput struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
	RiskLevel string `json:"risk_level"`
}

type mcpQueryDecisionsInput struct {
	RiskLevel string `json:"riskLevel,omitempty" jsonschema:"description=Filter to decisions matching this risk level"`
}

type mcpQueryDecisionsOutput struct {
	Decisions []domain.Decision `json:"decisions"`
}

type mcpQueryPlaybookInput struct {
	Trigger string `json:"trigger,omitempty" jsonschema:"description=Trigger label to look up"`
	Service string `json:"service,omitempty" jsonschema:"description=Service scope to filter by"`
}

type mcpQueryPlaybookOutput struct {
	Playbooks []domain.Playbook `json:"playbooks"`
}

type mcpSynthesizePlaybooksInput struct {
	MinLessons    int     `json:"minLessons,omitempty" jsonschema:"description=Minimum corroborating lessons per cluster (default 1)"`
	MinConfidence float64 `json:"minConfidence,omitempty" jsonschema:"description=Minimum confidence floor in [0, 1] (default 0.55)"`
}

type mcpSynthesizePlaybooksOutput struct {
	TriggerClusters  int      `json:"trigger_clusters"`
	PlaybooksEmitted int      `json:"playbooks_emitted"`
	Skipped          int      `json:"skipped"`
	PlaybookIDs      []string `json:"playbook_ids"`
}

// handleMCP starts the MCP server over stdio. This is a long-lived process
// that blocks until the connection is closed.
func handleMCP() {
	logger := bolt.New(bolt.NewJSONHandler(os.Stderr))

	// Resolve the actor once at startup from MNEMOS_USER_ID; every
	// persistence path below stamps it as created_by / changed_by. We
	// only validate against the DB when the env var is non-empty — an
	// unset env reliably means "attribute to <system>" with no lookup.
	mcpActor := resolveMCPActor()

	// When launched inside a project (.mnemos/ exists), bulk-ingest the
	// standard project documents so the agent has context immediately. New
	// or unchanged source paths are skipped, so this is safe to run on
	// every startup.
	if _, projectRoot, ok := findProjectDB(); ok {
		runAutoIngest(projectRoot, mcpActor)
		if repoIsGit(projectRoot) {
			runGitContextIngest(projectRoot, mcpActor)
			runPRContextIngest(projectRoot, mcpActor)
		}
	}

	srv := mcp.NewServer(mcp.ServerInfo{
		Name:    "mnemos",
		Version: version,
		Capabilities: mcp.Capabilities{
			Tools: true,
		},
	},
		mcp.WithTitle("Mnemos MCP Server"),
		mcp.WithDescription("Query and update evidence-backed local knowledge with Mnemos."),
		mcp.WithWebsiteURL("https://github.com/felixgeelhaar/mnemos"),
		mcp.WithBuildInfo(commit, buildDate),
		mcp.WithInstructions("Use query_knowledge to read the knowledge base, process_text to ingest raw text, and watch_file to keep a specific file's claims fresh as it changes. Prefer process_text before querying when no knowledge exists yet."),
	)

	// watch_file uses a long-lived DB connection separate from the
	// per-call connections in the other handlers. Opened lazily so
	// startup doesn't fail just because the watcher isn't needed.
	// We also remember the DB handle so the shutdown defer can close
	// it after stopping the polling goroutine — without this the
	// watcher leaks a connection on every MCP exit.
	var (
		watcherOnce sync.Once
		watcher     *Watcher
		watcherConn *store.Conn
		watcherErr  error
	)
	getWatcher := func() (*Watcher, error) {
		watcherOnce.Do(func() {
			// Background context is fine here: the open is a one-shot
			// per process and the long-lived Conn lifecycle is governed
			// by the deferred closeConn below, not by request-scoped
			// cancellation.
			conn, err := openConn(context.Background())
			if err != nil {
				watcherErr = err
				return
			}
			watcherConn = conn
			watcher = NewWatcher(conn, mcpActor)
		})
		return watcher, watcherErr
	}

	// Build the axi-go kernel that wraps every MCP tool with effect
	// gating, an evidence chain, and an execution budget. If the
	// kernel fails to build the MCP server still starts — we fall
	// back to direct dispatch so a kernel bug never blocks the agent.
	kernel, kernelErr := buildMCPKernel(logger, mcpExecutorMap(mcpActor, getWatcher))
	if kernelErr != nil {
		fmt.Fprintf(os.Stderr, "mcp: axi-go kernel disabled: %v\n", kernelErr)
	}

	srv.Tool("query_knowledge").
		Description("Query the Mnemos knowledge base and return evidence-backed results.").
		OutputSchema(mcpQueryOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpQueryInput) (mcpQueryOutput, error) {
			if kernel != nil {
				return dispatchAxiTool[mcpQueryOutput](ctx, kernel, nil, "query_knowledge", input)
			}
			return mcpRunQuery(ctx, input)
		})

	srv.Tool("process_text").
		Description("Ingest raw text, extract claims, detect relationships, and optionally generate embeddings.").
		OutputSchema(mcpProcessTextOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpProcessTextInput) (mcpProcessTextOutput, error) {
			if kernel != nil {
				return dispatchAxiTool[mcpProcessTextOutput](ctx, kernel, nil, "process_text", input)
			}
			return mcpRunProcessText(ctx, mcpActor, input)
		})

	srv.Tool("knowledge_metrics").
		Description("Return counts and statistics about the Mnemos knowledge base.").
		OutputSchema(mcpMetricsOutput{}).
		Handler(func(ctx context.Context, _ struct{}) (mcpMetricsOutput, error) {
			if kernel != nil {
				return dispatchAxiTool[mcpMetricsOutput](ctx, kernel, nil, "knowledge_metrics", struct{}{})
			}
			return mcpRunMetrics()
		})

	srv.Tool("list_claims").
		Description("List claims with optional type/status filtering and pagination. Useful for browsing the knowledge base without a specific question.").
		OutputSchema(mcpListClaimsOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpListClaimsInput) (mcpListClaimsOutput, error) {
			if kernel != nil {
				return dispatchAxiTool[mcpListClaimsOutput](ctx, kernel, nil, "list_claims", input)
			}
			return mcpRunListClaims(ctx, input)
		})

	srv.Tool("list_decisions").
		Description("List claims classified as decisions (shorthand for list_claims with type=decision).").
		OutputSchema(mcpListClaimsOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpListClaimsInput) (mcpListClaimsOutput, error) {
			input.Type = string(domain.ClaimTypeDecision)
			if kernel != nil {
				return dispatchAxiTool[mcpListClaimsOutput](ctx, kernel, nil, "list_decisions", input)
			}
			return mcpRunListClaims(ctx, input)
		})

	srv.Tool("list_contradictions").
		Description("List contradiction relationships hydrated with both claims' text. Pagination supported.").
		OutputSchema(mcpListContradictionsOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpListContradictionsInput) (mcpListContradictionsOutput, error) {
			if kernel != nil {
				return dispatchAxiTool[mcpListContradictionsOutput](ctx, kernel, nil, "list_contradictions", input)
			}
			return mcpRunListContradictions(ctx, input)
		})

	srv.Tool("ingest_git_prs").
		Description("Ingest merged GitHub pull requests from the project as events. Requires gh CLI authenticated for the repo's remote. Idempotent — already-ingested PR numbers are skipped.").
		OutputSchema(mcpIngestGitPRsOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpIngestGitPRsInput) (mcpIngestGitPRsOutput, error) {
			if kernel != nil {
				return dispatchAxiTool[mcpIngestGitPRsOutput](ctx, kernel, nil, "ingest_git_prs", input)
			}
			return mcpRunIngestGitPRs(ctx, mcpActor, input)
		})

	srv.Tool("ingest_git_log").
		Description("Ingest recent git commits from the project repository as events so they appear in queries. Idempotent — already-ingested commits are skipped by SHA.").
		OutputSchema(mcpIngestGitLogOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpIngestGitLogInput) (mcpIngestGitLogOutput, error) {
			if kernel != nil {
				return dispatchAxiTool[mcpIngestGitLogOutput](ctx, kernel, nil, "ingest_git_log", input)
			}
			return mcpRunIngestGitLog(ctx, mcpActor, input)
		})

	srv.Tool("record_action").
		Description("Record an operational action (deploy, rollback, scale, etc.) so it can be paired with later Outcomes for the synthesis layer. Idempotent on id.").
		OutputSchema(mcpRecordActionOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpRecordActionInput) (mcpRecordActionOutput, error) {
			return mcpRunRecordAction(ctx, mcpActor, input)
		})

	srv.Tool("record_outcome").
		Description("Record the observed outcome of a previously recorded Action, including numeric metrics. Idempotent on id.").
		OutputSchema(mcpRecordOutcomeOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpRecordOutcomeInput) (mcpRecordOutcomeOutput, error) {
			return mcpRunRecordOutcome(ctx, mcpActor, input)
		})

	srv.Tool("synthesize_lessons").
		Description("Run one full synthesis pass over actions+outcomes and emit derived Lessons. Idempotent: re-running on the same data refreshes confidence without churning ids.").
		OutputSchema(mcpSynthesizeOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpSynthesizeInput) (mcpSynthesizeOutput, error) {
			return mcpRunSynthesize(ctx, input)
		})

	srv.Tool("query_lessons").
		Description("Return validated lessons (synthesised operational knowledge) optionally filtered by service or trigger. Lessons are evidence-backed: each carries the action ids that corroborated it and a confidence in [0, 1].").
		OutputSchema(mcpQueryLessonsOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpQueryLessonsInput) (mcpQueryLessonsOutput, error) {
			return mcpRunQueryLessons(ctx, input)
		})

	srv.Tool("which_test_to_trust").
		Description("Rank every test_result claim sharing a TestRequirementRef by epistemic credibility (recency, pass-ratio, authority, citations) and return the winner with rationale. Use when CI shows divergent results for the same requirement and the agent must decide which test to believe.").
		OutputSchema(mcpWhichTestToTrustOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpWhichTestToTrustInput) (mcpWhichTestToTrustOutput, error) {
			return mcpRunWhichTestToTrust(ctx, input)
		})

	srv.Tool("record_decision").
		Description("Record a decision: the belief claims that justified it, the plan chosen, the alternatives considered, and the risk level. Optional outcomeId attaches an already-observed Outcome.").
		OutputSchema(mcpRecordDecisionOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpRecordDecisionInput) (mcpRecordDecisionOutput, error) {
			return mcpRunRecordDecision(ctx, mcpActor, input)
		})

	srv.Tool("query_decisions").
		Description("Return recorded decisions newest-first, optionally filtered by risk level. Each decision carries its belief claim ids, alternatives, and (when attached) the linked Outcome.").
		OutputSchema(mcpQueryDecisionsOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpQueryDecisionsInput) (mcpQueryDecisionsOutput, error) {
			return mcpRunQueryDecisions(ctx, input)
		})

	srv.Tool("query_playbook").
		Description("Return playbooks (steps-only operational intelligence) by trigger or service scope. Mnemos returns steps; execution is the caller's responsibility — Praxis consumes this contract.").
		OutputSchema(mcpQueryPlaybookOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpQueryPlaybookInput) (mcpQueryPlaybookOutput, error) {
			return mcpRunQueryPlaybook(ctx, input)
		})

	srv.Tool("synthesize_playbooks").
		Description("Run one full playbook-synthesis pass over the lessons store and emit derived Playbooks. Idempotent: re-running on the same lessons refreshes confidence without churning ids.").
		OutputSchema(mcpSynthesizePlaybooksOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpSynthesizePlaybooksInput) (mcpSynthesizePlaybooksOutput, error) {
			return mcpRunSynthesizePlaybooks(ctx, input)
		})

	srv.Tool("watch_file").
		Description("Register a file to be re-ingested when its content changes. Polls every few seconds; in-memory only — restart drops all watches.").
		OutputSchema(mcpWatchFileOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpWatchFileInput) (mcpWatchFileOutput, error) {
			if kernel != nil {
				return dispatchAxiTool[mcpWatchFileOutput](ctx, kernel, nil, "watch_file", input)
			}
			out, err := runWatchFileTool(input, getWatcher)
			if err != nil {
				return mcpWatchFileOutput{}, err
			}
			return out, nil
		})

	// --- Self-editing memory tools (letta-style) ---
	srv.Tool("memory_deprecate").
		Description("Mark a claim as deprecated when the agent finds it stale or wrong. Records the rationale on the status transition; existing evidence + history stay queryable.").
		OutputSchema(mcpMemoryDeprecateOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpMemoryDeprecateInput) (mcpMemoryDeprecateOutput, error) {
			return mcpRunMemoryDeprecate(ctx, mcpActor, input)
		})

	srv.Tool("memory_resolve_contradiction").
		Description("Pick the winner of two contradicting claims. Winner moves to status=resolved; loser to status=deprecated. Both transitions carry the rationale.").
		OutputSchema(mcpMemoryResolveOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpMemoryResolveInput) (mcpMemoryResolveOutput, error) {
			return mcpRunMemoryResolve(ctx, mcpActor, input)
		})

	srv.Tool("memory_escalate").
		Description("Signal that the agent cannot resolve a claim autonomously and requests human review. Records an escalation Verdict on the claim with the agent-provided reason so the audit trail captures who escalated and why.").
		OutputSchema(mcpMemoryEscalateOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpMemoryEscalateInput) (mcpMemoryEscalateOutput, error) {
			return mcpRunMemoryEscalate(ctx, mcpActor, input)
		})

	srv.Tool("memory_promote").
		Description("Re-verify a claim against fresh evidence. Bumps last_verified, increments verify_count — the trust score follows.").
		OutputSchema(mcpMemoryPromoteOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpMemoryPromoteInput) (mcpMemoryPromoteOutput, error) {
			return mcpRunMemoryPromote(ctx, mcpActor, input)
		})

	srv.Tool("memory_context").
		Description("Render the system-prompt-ready Context Block for a run: top claims by trust, surfaced contradictions, footer with counts. Drop directly into the agent's prompt at the start of each turn.").
		OutputSchema(mcpMemoryContextOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpMemoryContextInput) (mcpMemoryContextOutput, error) {
			return mcpRunMemoryContext(ctx, input)
		})

	// --- Agent memory-management primitives (Refs #41) ---
	srv.Tool("remember").
		Description("Store a single fact as a claim, scoped to a run_id, with an event + evidence link so it is auditable. Use this when the agent decides to commit something to long-term memory.").
		OutputSchema(mcpRememberOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpRememberInput) (mcpRememberOutput, error) {
			return mcpRunRemember(ctx, mcpActor, input)
		})

	srv.Tool("forget").
		Description("Soft-delete a claim by flipping its status to deprecated. The claim and its evidence stay queryable for audit; future recall paths exclude it from active context.").
		OutputSchema(mcpForgetOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpForgetInput) (mcpForgetOutput, error) {
			return mcpRunForget(ctx, mcpActor, input)
		})

	srv.Tool("update").
		Description("Rewrite a claim's text (and optionally its confidence) when the agent's understanding refines. The reason is recorded in the status history.").
		OutputSchema(mcpUpdateOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpUpdateInput) (mcpUpdateOutput, error) {
			return mcpRunUpdate(ctx, mcpActor, input)
		})

	srv.Tool("search_memory").
		Description("Semantic search over the agent's memory: embeds the query, ranks claims by cosine similarity, scoped to a run_id (tenant boundary).").
		OutputSchema(mcpSearchMemoryOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpSearchMemoryInput) (mcpSearchMemoryOutput, error) {
			return mcpRunSearchMemory(ctx, input)
		})

	srv.Tool("remember_event").
		Description("Store a temporal event (deployment, incident, decision, ...) with a wall-clock timestamp.").
		OutputSchema(mcpRememberEventOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpRememberEventInput) (mcpRememberEventOutput, error) {
			if kernel != nil {
				return dispatchAxiTool[mcpRememberEventOutput](ctx, kernel, nil, "remember_event", input)
			}
			return mcpRunRememberEvent(ctx, mcpActor, input)
		})

	srv.Tool("timeline_query").
		Description("Return events filtered by time range, type, and run, sorted chronologically.").
		OutputSchema(mcpTimelineQueryOutput{}).
		Handler(func(ctx context.Context, input mcpTimelineQueryInput) (mcpTimelineQueryOutput, error) {
			if kernel != nil {
				return dispatchAxiTool[mcpTimelineQueryOutput](ctx, kernel, nil, "timeline_query", input)
			}
			return mcpRunTimelineQuery(ctx, input)
		})

	srv.Tool("recall_at_time").
		Description("Answer a question against the state of the knowledge base at a historical instant.").
		OutputSchema(mcpQueryOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, input mcpRecallAtTimeInput) (mcpQueryOutput, error) {
			if kernel != nil {
				return dispatchAxiTool[mcpQueryOutput](ctx, kernel, nil, "recall_at_time", input)
			}
			return mcpRunRecallAtTime(ctx, input)
		})

	// Wire signal handling so a SIGINT/SIGTERM cancels the parent
	// context: ServeStdio observes the cancellation and returns,
	// then we tear the watcher down. Without this, Ctrl+C would
	// leave the polling goroutine alive and the DB unflushed.
	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopSignals := make(chan os.Signal, 1)
	signal.Notify(stopSignals, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig, ok := <-stopSignals
		if !ok {
			return
		}
		fmt.Fprintf(os.Stderr, "mcp: received %s, shutting down...\n", sig)
		cancel()
	}()

	// Defer watcher shutdown so the polling goroutine exits and the
	// DB connection it holds gets released. Cheap if no watcher was
	// ever started.
	defer func() {
		if watcher != nil {
			watcher.Stop()
		}
		if watcherConn != nil {
			_ = watcherConn.Close()
		}
	}()

	if err := mcp.ServeStdio(rootCtx, srv, mcp.WithMiddleware(mcp.DefaultMiddlewareWithTimeout(mcpBoltLogger{logger: logger}, 30*time.Second)...)); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func runGitContextIngest(projectRoot, actor string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := openConn(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "git: failed to open DB: %v\n", err)
		return
	}
	defer closeConn(conn)

	ingested, skipped, err := ingestGitLog(ctx, conn, projectRoot, defaultGitLogLimit, "", actor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "git: %v\n", err)
		return
	}
	if ingested > 0 || skipped > 0 {
		fmt.Fprintf(os.Stderr, "git-context: ingested=%d skipped=%d root=%s\n", ingested, skipped, projectRoot)
	}
}

func runPRContextIngest(projectRoot, actor string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Avoid shelling out to gh if it isn't available or isn't authed —
	// ghAvailable is the cheap probe.
	if !ghAvailable(ctx) {
		return
	}

	conn, err := openConn(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "git-prs: failed to open DB: %v\n", err)
		return
	}
	defer closeConn(conn)

	ingested, skipped, err := ingestGhPRs(ctx, conn, projectRoot, defaultGitPRLimit, actor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "git-prs: %v\n", err)
		return
	}
	if ingested > 0 || skipped > 0 {
		fmt.Fprintf(os.Stderr, "git-prs: ingested=%d skipped=%d root=%s\n", ingested, skipped, projectRoot)
	}
}

func mcpRunIngestGitPRs(ctx context.Context, actor string, input mcpIngestGitPRsInput) (mcpIngestGitPRsOutput, error) {
	_, projectRoot, ok := findProjectDB()
	if !ok {
		return mcpIngestGitPRsOutput{}, fmt.Errorf("no project (.mnemos/) found — run 'mnemos init' first")
	}
	if !ghAvailable(ctx) {
		return mcpIngestGitPRsOutput{}, fmt.Errorf("gh CLI not installed or not authenticated for github.com")
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpIngestGitPRsOutput{}, err
	}
	defer closeConn(conn)

	ingested, skipped, err := ingestGhPRs(ctx, conn, projectRoot, input.Limit, actor)
	if err != nil {
		return mcpIngestGitPRsOutput{}, err
	}
	return mcpIngestGitPRsOutput{Ingested: ingested, Skipped: skipped}, nil
}

func mcpRunIngestGitLog(ctx context.Context, actor string, input mcpIngestGitLogInput) (mcpIngestGitLogOutput, error) {
	_, projectRoot, ok := findProjectDB()
	if !ok {
		return mcpIngestGitLogOutput{}, fmt.Errorf("no project (.mnemos/) found — run 'mnemos init' first")
	}
	if !repoIsGit(projectRoot) {
		return mcpIngestGitLogOutput{}, fmt.Errorf("project root %s is not a git repository", projectRoot)
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpIngestGitLogOutput{}, err
	}
	defer closeConn(conn)

	ingested, skipped, err := ingestGitLog(ctx, conn, projectRoot, input.Limit, input.Since, actor)
	if err != nil {
		return mcpIngestGitLogOutput{}, err
	}
	return mcpIngestGitLogOutput{Ingested: ingested, Skipped: skipped}, nil
}

func runAutoIngest(projectRoot, actor string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := openConn(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auto-ingest: failed to open DB: %v\n", err)
		return
	}
	defer closeConn(conn)

	report := autoIngestProjectDocs(ctx, conn, projectRoot, actor)
	if report.Ingested > 0 || report.Skipped > 0 || report.HasFailures() {
		fmt.Fprintf(os.Stderr, "auto-ingest: ingested=%d skipped=%d failed=%d root=%s\n",
			report.Ingested, report.Skipped, len(report.PerFileErrors), projectRoot)
	}
	if report.DedupeFailed {
		fmt.Fprintln(os.Stderr, "auto-ingest: warning — dedupe lookup failed; nothing was ingested to avoid duplicate runs")
	}
	if report.ExtractorError != nil {
		fmt.Fprintf(os.Stderr, "auto-ingest: warning — extractor build failed (%v); nothing was attempted\n", report.ExtractorError)
	}
}

// resolveMCPActor reads MNEMOS_USER_ID at MCP startup. Empty env ->
// SystemUser. Non-empty env -> validated against the local DB so typos
// surface immediately instead of silently stamping a nonexistent user.
// A lookup failure here logs a warning and falls back to SystemUser,
// since the MCP process shouldn't refuse to start over an auth config
// mistake — downstream writes will just carry the fallback attribution.
func resolveMCPActor() string {
	candidate := strings.TrimSpace(os.Getenv("MNEMOS_USER_ID"))
	if candidate == "" {
		return domain.SystemUser
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := openConn(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp: MNEMOS_USER_ID=%s but couldn't open DB to validate: %v — using <system>\n", candidate, err)
		return domain.SystemUser
	}
	defer closeConn(conn)

	actor, err := resolveActor(ctx, conn.Users, candidate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp: MNEMOS_USER_ID=%s rejected: %v — using <system>\n", candidate, err)
		return domain.SystemUser
	}
	return actor
}

func mcpRunQuery(ctx context.Context, input mcpQueryInput) (mcpQueryOutput, error) {
	conn, err := openConn(ctx)
	if err != nil {
		return mcpQueryOutput{}, err
	}
	defer closeConn(conn)

	engine := query.NewEngine(conn.Events, conn.Claims, conn.Relationships)

	// Enable semantic ranking when an embedding provider is configured —
	// auto-detected via Ollama or env vars. Without this the engine falls
	// back to token-overlap ranking and semantic matches are missed even
	// when the DB has embeddings.
	if embCfg, err := embedding.ConfigFromEnv(); err == nil {
		if embClient, err := embedding.NewClient(embCfg); err == nil {
			engine = engine.WithEmbeddings(conn.Embeddings, embClient)
		}
	}

	if llmCfg, err := llm.ConfigFromEnv(); err == nil {
		if llmClient, err := llm.NewClient(llmCfg); err == nil {
			engine = engine.WithLLM(llmClient)
		}
	}

	hops := input.Hops
	if hops < 0 {
		hops = 0
	}
	if hops > 5 {
		hops = 5
	}
	opts := query.AnswerOptions{Hops: hops}

	var answer domain.Answer
	if strings.TrimSpace(input.RunID) != "" {
		answer, err = engine.AnswerForRunWithOptions(strings.TrimSpace(input.Question), strings.TrimSpace(input.RunID), opts)
	} else {
		answer, err = engine.AnswerWithOptions(strings.TrimSpace(input.Question), opts)
	}
	if err != nil {
		return mcpQueryOutput{}, err
	}

	return mcpQueryOutput{
		Answer:           answer.AnswerText,
		Claims:           answer.Claims,
		Contradictions:   answer.Contradictions,
		Timeline:         answer.TimelineEventIDs,
		ClaimProvenance:  answer.ClaimProvenance,
		ClaimHopDistance: answer.ClaimHopDistance,
	}, nil
}

func mcpRunProcessText(ctx context.Context, actor string, input mcpProcessTextInput) (mcpProcessTextOutput, error) {
	service := ingest.NewService()
	normalizer := parser.NewNormalizer()
	progress := mcp.ProgressFromContext(ctx)

	conn, err := openConn(ctx)
	if err != nil {
		return mcpProcessTextOutput{}, err
	}
	defer closeConn(conn)

	runner := workflow.NewRunner(conn.Jobs)
	runner.Timeout = 30 * time.Second
	runner.MaxRetries = 1

	var result mcpProcessTextOutput
	err = runner.Run("process", map[string]string{"source": "raw_text", "mcp": "true"}, func(ctx context.Context, job *workflow.Job) error {
		total := 5.0
		_ = progress.Report(0, &total)

		if err := job.SetStatus("loading", ""); err != nil {
			return err
		}
		raw := strings.TrimSpace(input.Text)
		in, content, err := service.IngestText(raw, nil)
		if err != nil {
			return err
		}
		_ = progress.Report(1, &total)

		if err := job.SetStatus("extracting", ""); err != nil {
			return err
		}
		events, err := normalizer.Normalize(in, content)
		if err != nil {
			return err
		}
		for i := range events {
			events[i].RunID = job.ID()
		}

		ext, err := pipeline.NewExtractor(input.UseLLM)
		if err != nil {
			return err
		}
		claims, links, mcpEntities, err := ext.ExtractFn(events)
		if err != nil {
			return err
		}
		_ = progress.Report(2, &total)
		// MCP path defers entity materialisation to the
		// post-PersistArtifacts step at the end of this handler so
		// the audit/persist transaction stays focused on artifacts.
		_ = mcpEntities

		if err := job.SetStatus("relating", ""); err != nil {
			return err
		}
		relEngine := relate.NewEngine()
		rels, err := relEngine.Detect(claims)
		if err != nil {
			return err
		}

		existingClaims, err := conn.Claims.ListAll(ctx)
		if err != nil {
			return err
		}
		if len(existingClaims) > 0 {
			incrementalRels, err := relEngine.DetectIncremental(claims, existingClaims)
			if err != nil {
				return err
			}
			rels = append(rels, incrementalRels...)
		}
		_ = progress.Report(3, &total)

		if err := job.SetStatus("saving", ""); err != nil {
			return err
		}
		stampEventActor(events, actor)
		stampClaimActor(claims, actor)
		stampRelationshipActor(rels, actor)
		if err := pipeline.PersistArtifacts(ctx, conn, events, claims, links, rels); err != nil {
			return err
		}
		// Best-effort entity materialisation; failures are logged
		// but don't abort the MCP response. The agent caller cares
		// about the answer; entity tagging is enrichment that can
		// be backfilled via `mnemos extract-entities`.
		if _, entErr := pipeline.MaterializeEntities(ctx, conn, mcpEntities, actor); entErr != nil {
			fmt.Fprintf(os.Stderr, "  entity materialisation (mcp) failed: %v\n", entErr)
		}

		embeddingCount := 0
		if input.UseEmbeddings {
			if err := job.SetStatus("embedding", ""); err != nil {
				return err
			}
			embeddingCount, err = pipeline.GenerateEmbeddings(ctx, conn, events)
			if err != nil {
				return err
			}
			claimEmbCount, claimErr := pipeline.GenerateClaimEmbeddings(ctx, conn, claims)
			if claimErr != nil {
				return claimErr
			}
			embeddingCount += claimEmbCount
		}
		_ = progress.Report(5, &total)

		result = mcpProcessTextOutput{
			RunID:          job.ID(),
			Events:         len(events),
			Claims:         len(claims),
			Relationships:  len(rels),
			Embeddings:     embeddingCount,
			UsedLLM:        input.UseLLM,
			UsedEmbeddings: input.UseEmbeddings,
		}
		return nil
	})
	if err != nil {
		return mcpProcessTextOutput{}, err
	}

	return result, nil
}

func mcpRunMetrics() (mcpMetricsOutput, error) {
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		return mcpMetricsOutput{}, err
	}
	defer closeConn(conn)

	// Each port-typed CountAll/CountByType replaces a hand-written
	// COUNT query. Distinct-run-id is computed in-memory from
	// ListAll because adding a CountDistinctRunID port method just
	// for the metrics surface isn't worth the surface bloat.
	allEvents, _ := conn.Events.ListAll(ctx)
	runIDs := map[string]struct{}{}
	for _, e := range allEvents {
		if e.RunID != "" {
			runIDs[e.RunID] = struct{}{}
		}
	}

	eventsCount, _ := conn.Events.CountAll(ctx)
	claimsCount, _ := conn.Claims.CountAll(ctx)
	relsCount, _ := conn.Relationships.CountAll(ctx)
	contradictionsCount, _ := conn.Relationships.CountByType(ctx, "contradicts")
	embeddingsCount, _ := conn.Embeddings.CountAll(ctx)

	// Contested claims: count from the in-memory listing — same
	// reasoning as runs, the surface payoff doesn't justify a
	// CountByStatus port method.
	allClaims, _ := conn.Claims.ListAll(ctx)
	var contested int64
	for _, c := range allClaims {
		if string(c.Status) == "contested" {
			contested++
		}
	}

	return mcpMetricsOutput{
		Runs:            int64(len(runIDs)),
		Events:          eventsCount,
		Claims:          claimsCount,
		ContestedClaims: contested,
		Relationships:   relsCount,
		Contradictions:  contradictionsCount,
		Embeddings:      embeddingsCount,
	}, nil
}

func mcpRunRecordAction(ctx context.Context, actor string, input mcpRecordActionInput) (mcpRecordActionOutput, error) {
	at := time.Now().UTC()
	if strings.TrimSpace(input.At) != "" {
		t, err := parseTimeArg(input.At)
		if err != nil {
			return mcpRecordActionOutput{}, fmt.Errorf("at: %w", err)
		}
		at = t
	}
	id, err := newID("ac_")
	if err != nil {
		return mcpRecordActionOutput{}, fmt.Errorf("generate action id: %w", err)
	}
	resolvedActor := input.Actor
	if resolvedActor == "" {
		resolvedActor = actor
	}
	action := domain.Action{
		ID:        id,
		RunID:     input.RunID,
		Kind:      domain.ActionKind(input.Kind),
		Subject:   input.Subject,
		Actor:     resolvedActor,
		At:        at,
		Metadata:  input.Metadata,
		CreatedBy: actor,
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpRecordActionOutput{}, err
	}
	defer closeConn(conn)
	if err := conn.Actions.Append(ctx, action); err != nil {
		return mcpRecordActionOutput{}, err
	}
	return mcpRecordActionOutput{ID: action.ID, Kind: string(action.Kind), Subject: action.Subject}, nil
}

func mcpRunRecordDecision(ctx context.Context, actor string, input mcpRecordDecisionInput) (mcpRecordDecisionOutput, error) {
	chosenAt := time.Now().UTC()
	if strings.TrimSpace(input.ChosenAt) != "" {
		t, err := parseTimeArg(input.ChosenAt)
		if err != nil {
			return mcpRecordDecisionOutput{}, fmt.Errorf("chosenAt: %w", err)
		}
		chosenAt = t
	}
	id, err := newID("dc_")
	if err != nil {
		return mcpRecordDecisionOutput{}, fmt.Errorf("generate decision id: %w", err)
	}
	d := domain.Decision{
		ID:           id,
		Statement:    input.Statement,
		Plan:         input.Plan,
		Reasoning:    input.Reasoning,
		RiskLevel:    domain.RiskLevel(input.RiskLevel),
		Beliefs:      input.Beliefs,
		Alternatives: input.Alternatives,
		OutcomeID:    input.OutcomeID,
		ChosenAt:     chosenAt,
		CreatedBy:    actor,
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpRecordDecisionOutput{}, err
	}
	defer closeConn(conn)
	if err := conn.Decisions.Append(ctx, d); err != nil {
		return mcpRecordDecisionOutput{}, err
	}
	return mcpRecordDecisionOutput{ID: d.ID, Statement: d.Statement, RiskLevel: string(d.RiskLevel)}, nil
}

func mcpRunQueryPlaybook(ctx context.Context, input mcpQueryPlaybookInput) (mcpQueryPlaybookOutput, error) {
	conn, err := openConn(ctx)
	if err != nil {
		return mcpQueryPlaybookOutput{}, err
	}
	defer closeConn(conn)
	var ps []domain.Playbook
	switch {
	case input.Trigger != "":
		ps, err = conn.Playbooks.ListByTrigger(ctx, input.Trigger)
	case input.Service != "":
		ps, err = conn.Playbooks.ListByService(ctx, input.Service)
	default:
		ps, err = conn.Playbooks.ListAll(ctx)
	}
	if err != nil {
		return mcpQueryPlaybookOutput{}, err
	}
	return mcpQueryPlaybookOutput{Playbooks: ps}, nil
}

func mcpRunSynthesizePlaybooks(ctx context.Context, input mcpSynthesizePlaybooksInput) (mcpSynthesizePlaybooksOutput, error) {
	conn, err := openConn(ctx)
	if err != nil {
		return mcpSynthesizePlaybooksOutput{}, err
	}
	defer closeConn(conn)
	res, err := synthesize.Playbooks(ctx, conn.Lessons, conn.Playbooks, synthesize.PlaybookOptions{
		MinLessons:    input.MinLessons,
		MinConfidence: input.MinConfidence,
	})
	if err != nil {
		return mcpSynthesizePlaybooksOutput{}, err
	}
	return mcpSynthesizePlaybooksOutput{
		TriggerClusters:  res.TriggerClusters,
		PlaybooksEmitted: res.PlaybooksEmitted,
		Skipped:          res.Skipped,
		PlaybookIDs:      res.PlaybookIDs,
	}, nil
}

func mcpRunQueryDecisions(ctx context.Context, input mcpQueryDecisionsInput) (mcpQueryDecisionsOutput, error) {
	conn, err := openConn(ctx)
	if err != nil {
		return mcpQueryDecisionsOutput{}, err
	}
	defer closeConn(conn)
	var ds []domain.Decision
	if input.RiskLevel != "" {
		ds, err = conn.Decisions.ListByRiskLevel(ctx, input.RiskLevel)
	} else {
		ds, err = conn.Decisions.ListAll(ctx)
	}
	if err != nil {
		return mcpQueryDecisionsOutput{}, err
	}
	return mcpQueryDecisionsOutput{Decisions: ds}, nil
}

func mcpRunQueryLessons(ctx context.Context, input mcpQueryLessonsInput) (mcpQueryLessonsOutput, error) {
	conn, err := openConn(ctx)
	if err != nil {
		return mcpQueryLessonsOutput{}, err
	}
	defer closeConn(conn)
	var ls []domain.Lesson
	switch {
	case input.Service != "":
		ls, err = conn.Lessons.ListByService(ctx, input.Service)
	case input.Trigger != "":
		ls, err = conn.Lessons.ListByTrigger(ctx, input.Trigger)
	default:
		ls, err = conn.Lessons.ListAll(ctx)
	}
	if err != nil {
		return mcpQueryLessonsOutput{}, err
	}
	return mcpQueryLessonsOutput{Lessons: ls}, nil
}

func mcpRunWhichTestToTrust(ctx context.Context, input mcpWhichTestToTrustInput) (mcpWhichTestToTrustOutput, error) {
	if input.RequirementRef == "" {
		return mcpWhichTestToTrustOutput{}, fmt.Errorf("requirement_ref is required")
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpWhichTestToTrustOutput{}, err
	}
	defer closeConn(conn)

	matches, err := conn.Claims.ListByTestRequirementRef(ctx, input.RequirementRef)
	if err != nil {
		return mcpWhichTestToTrustOutput{}, err
	}

	scopeFilter := domain.Scope{Service: input.Service, Env: input.Env, Team: input.Team}

	now := time.Now().UTC()
	cands := make([]mcpWhichTestToTrustCandidate, 0)
	for _, c := range matches {
		if !scopeFilter.IsEmpty() && !c.Scope.Matches(scopeFilter) {
			continue
		}
		score, _, rationale, prose := trust.BuildReport(trust.CredibilityInputs{
			CurrentTrust:    c.TrustScore,
			SourceAuthority: c.SourceAuthority,
			Liveness:        c.Liveness,
			CitationCount:   c.CitationCount,
			LastExecuted:    c.LastExecuted,
			LastVerified:    c.LastVerified,
			ValidFrom:       c.ValidFrom,
			CreatedAt:       c.CreatedAt,
			Now:             now,
			IsTest:          true,
			TestLastRunAt:   c.TestLastRunAt,
			TestPassCount:   c.TestPassCount,
			TestFailCount:   c.TestFailCount,
		})
		lastRun := ""
		if !c.TestLastRunAt.IsZero() {
			lastRun = c.TestLastRunAt.UTC().Format(time.RFC3339)
		}
		cands = append(cands, mcpWhichTestToTrustCandidate{
			ClaimID:        c.ID,
			Text:           c.Text,
			TestID:         c.TestID,
			TestAuthor:     c.TestAuthor,
			TestLastRunAt:  lastRun,
			TestPassCount:  c.TestPassCount,
			TestFailCount:  c.TestFailCount,
			Score:          score,
			Rationale:      rationale,
			ProseRationale: prose,
		})
	}

	if len(cands) == 0 {
		return mcpWhichTestToTrustOutput{
			RequirementRef: input.RequirementRef,
			Verdict:        "no_candidates",
			Candidates:     []mcpWhichTestToTrustCandidate{},
		}, nil
	}

	sort.Slice(cands, func(i, j int) bool { return cands[i].Score > cands[j].Score })

	verdict := "winner"
	if len(cands) > 1 && cands[0].Score-cands[1].Score < 0.05 {
		verdict = "ambiguous"
	}
	return mcpWhichTestToTrustOutput{
		RequirementRef: input.RequirementRef,
		Verdict:        verdict,
		WinnerClaimID:  cands[0].ClaimID,
		WinnerScore:    cands[0].Score,
		Candidates:     cands,
	}, nil
}

func mcpRunSynthesize(ctx context.Context, input mcpSynthesizeInput) (mcpSynthesizeOutput, error) {
	conn, err := openConn(ctx)
	if err != nil {
		return mcpSynthesizeOutput{}, err
	}
	defer closeConn(conn)
	res, err := synthesize.Synthesize(ctx, conn.Actions, conn.Outcomes, conn.Lessons, synthesize.Options{
		MinCorroboration: input.MinCorroboration,
		MinConfidence:    input.MinConfidence,
	})
	if err != nil {
		return mcpSynthesizeOutput{}, err
	}
	return mcpSynthesizeOutput{
		Clusters:       res.Clusters,
		LessonsEmitted: res.LessonsEmitted,
		Skipped:        res.Skipped,
		LessonIDs:      res.LessonIDs,
	}, nil
}

func mcpRunRecordOutcome(ctx context.Context, actor string, input mcpRecordOutcomeInput) (mcpRecordOutcomeOutput, error) {
	observed := time.Now().UTC()
	if strings.TrimSpace(input.ObservedAt) != "" {
		t, err := parseTimeArg(input.ObservedAt)
		if err != nil {
			return mcpRecordOutcomeOutput{}, fmt.Errorf("observedAt: %w", err)
		}
		observed = t
	}
	source := input.Source
	if source == "" {
		source = "push"
	}
	id, err := newID("oc_")
	if err != nil {
		return mcpRecordOutcomeOutput{}, fmt.Errorf("generate outcome id: %w", err)
	}
	outcome := domain.Outcome{
		ID:         id,
		ActionID:   input.ActionID,
		Result:     domain.OutcomeResult(input.Result),
		Metrics:    input.Metrics,
		Notes:      input.Notes,
		ObservedAt: observed,
		Source:     source,
		CreatedBy:  actor,
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpRecordOutcomeOutput{}, err
	}
	defer closeConn(conn)
	if err := conn.Outcomes.Append(ctx, outcome); err != nil {
		return mcpRecordOutcomeOutput{}, err
	}
	if err := autoedge.OnOutcomeAppended(ctx, conn.EntityRels, outcome, actor); err != nil {
		return mcpRecordOutcomeOutput{}, err
	}
	return mcpRecordOutcomeOutput{ID: outcome.ID, ActionID: outcome.ActionID, Result: string(outcome.Result)}, nil
}

// --- Self-editing memory tools (letta-style) ---------------------------------
//
// The four tools below let an LLM agent curate Mnemos's memory while it
// runs: deprecate a claim it discovers is stale, resolve a contradiction
// by picking a winner, and pull a system-prompt-ready Context Block at
// the start of each turn. The shape mirrors letta's core/recall/archival
// edit tools, mapped onto Mnemos's claim-status lifecycle.
//
// All four are stateless: each call resolves the claim by id, applies
// the change through the existing repository contracts, returns a small
// JSON receipt. No agent-internal state is kept.

type mcpMemoryDeprecateInput struct {
	ClaimID string `json:"claim_id" jsonschema:"required,description=Claim id to deprecate"`
	Reason  string `json:"reason,omitempty" jsonschema:"description=Free-form rationale stored on the status transition"`
}

type mcpMemoryDeprecateOutput struct {
	ClaimID   string `json:"claim_id"`
	OldStatus string `json:"old_status"`
	NewStatus string `json:"new_status"`
}

type mcpMemoryResolveInput struct {
	WinnerID string `json:"winner_id" jsonschema:"required,description=Claim id that should remain active"`
	LoserID  string `json:"loser_id" jsonschema:"required,description=Claim id that should be marked deprecated"`
	Reason   string `json:"reason,omitempty" jsonschema:"description=Free-form rationale stored on both status transitions"`
}

type mcpMemoryResolveOutput struct {
	WinnerID string `json:"winner_id"`
	LoserID  string `json:"loser_id"`
}

type mcpMemoryPromoteInput struct {
	ClaimID string `json:"claim_id" jsonschema:"required,description=Claim id to promote (re-verify against fresh evidence)"`
}

type mcpMemoryPromoteOutput struct {
	ClaimID    string `json:"claim_id"`
	VerifiedAt string `json:"verified_at"`
}

type mcpMemoryEscalateInput struct {
	ClaimID string `json:"claim_id" jsonschema:"required,description=ID of the claim the agent cannot resolve autonomously"`
	Reason  string `json:"reason,omitempty" jsonschema:"description=Why the agent is escalating this claim (shown verbatim in the audit trail)"`
}

type mcpMemoryEscalateOutput struct {
	ClaimID          string `json:"claim_id"`
	Action           string `json:"action"`
	EscalationReason string `json:"escalation_reason"`
	Rationale        string `json:"rationale"`
}

type mcpMemoryContextInput struct {
	RunID     string `json:"run_id" jsonschema:"required,description=Run id whose context block to render"`
	MaxTokens int    `json:"max_tokens,omitempty" jsonschema:"description=Approximate token budget (chars/4); 0 disables truncation"`
}

type mcpMemoryContextOutput struct {
	RunID   string `json:"run_id"`
	Context string `json:"context"`
}

func mcpRunMemoryEscalate(ctx context.Context, _ string, input mcpMemoryEscalateInput) (mcpMemoryEscalateOutput, error) {
	if input.ClaimID == "" {
		return mcpMemoryEscalateOutput{}, fmt.Errorf("claim_id is required")
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpMemoryEscalateOutput{}, err
	}
	defer closeConn(conn)
	eng := query.NewEngine(conn.Events, conn.Claims, conn.Relationships)
	verdict, err := eng.EscalateClaimForAgent(ctx, input.ClaimID, input.Reason)
	if err != nil {
		return mcpMemoryEscalateOutput{}, err
	}
	return mcpMemoryEscalateOutput{
		ClaimID:          input.ClaimID,
		Action:           string(verdict.Action),
		EscalationReason: verdict.EscalationReason,
		Rationale:        verdict.Rationale,
	}, nil
}

func mcpRunMemoryDeprecate(ctx context.Context, actor string, input mcpMemoryDeprecateInput) (mcpMemoryDeprecateOutput, error) {
	if input.ClaimID == "" {
		return mcpMemoryDeprecateOutput{}, fmt.Errorf("claim_id is required")
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpMemoryDeprecateOutput{}, err
	}
	defer closeConn(conn)
	existing, err := conn.Claims.ListByIDs(ctx, []string{input.ClaimID})
	if err != nil || len(existing) == 0 {
		return mcpMemoryDeprecateOutput{}, fmt.Errorf("claim %s not found", input.ClaimID)
	}
	old := existing[0].Status
	updated := existing[0]
	updated.Status = domain.ClaimStatusDeprecated
	reason := input.Reason
	if reason == "" {
		reason = "agent-deprecated"
	}
	if err := conn.Claims.UpsertWithReasonAs(ctx, []domain.Claim{updated}, reason, actor); err != nil {
		return mcpMemoryDeprecateOutput{}, err
	}
	return mcpMemoryDeprecateOutput{
		ClaimID:   input.ClaimID,
		OldStatus: string(old),
		NewStatus: string(domain.ClaimStatusDeprecated),
	}, nil
}

func mcpRunMemoryResolve(ctx context.Context, actor string, input mcpMemoryResolveInput) (mcpMemoryResolveOutput, error) {
	if input.WinnerID == "" || input.LoserID == "" {
		return mcpMemoryResolveOutput{}, fmt.Errorf("winner_id and loser_id are required")
	}
	if input.WinnerID == input.LoserID {
		return mcpMemoryResolveOutput{}, fmt.Errorf("winner_id and loser_id must differ")
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpMemoryResolveOutput{}, err
	}
	defer closeConn(conn)
	existing, err := conn.Claims.ListByIDs(ctx, []string{input.WinnerID, input.LoserID})
	if err != nil || len(existing) < 2 {
		return mcpMemoryResolveOutput{}, fmt.Errorf("both claims must exist")
	}
	reason := input.Reason
	if reason == "" {
		reason = "agent-resolved-contradiction"
	}
	updates := make([]domain.Claim, 0, 2)
	for _, c := range existing {
		switch c.ID {
		case input.WinnerID:
			c.Status = domain.ClaimStatusResolved
		case input.LoserID:
			c.Status = domain.ClaimStatusDeprecated
		}
		updates = append(updates, c)
	}
	if err := conn.Claims.UpsertWithReasonAs(ctx, updates, reason, actor); err != nil {
		return mcpMemoryResolveOutput{}, err
	}
	return mcpMemoryResolveOutput{WinnerID: input.WinnerID, LoserID: input.LoserID}, nil
}

func mcpRunMemoryPromote(ctx context.Context, actor string, input mcpMemoryPromoteInput) (mcpMemoryPromoteOutput, error) {
	_ = actor // attribution lives in the verify path's own log
	if input.ClaimID == "" {
		return mcpMemoryPromoteOutput{}, fmt.Errorf("claim_id is required")
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpMemoryPromoteOutput{}, err
	}
	defer closeConn(conn)
	now := time.Now().UTC()
	if err := conn.Claims.MarkVerified(ctx, input.ClaimID, now, 0); err != nil {
		return mcpMemoryPromoteOutput{}, err
	}
	return mcpMemoryPromoteOutput{
		ClaimID:    input.ClaimID,
		VerifiedAt: now.Format(time.RFC3339),
	}, nil
}

func mcpRunMemoryContext(ctx context.Context, input mcpMemoryContextInput) (mcpMemoryContextOutput, error) {
	if input.RunID == "" {
		return mcpMemoryContextOutput{}, fmt.Errorf("run_id is required")
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpMemoryContextOutput{}, err
	}
	defer closeConn(conn)
	engine := query.NewEngine(conn.Events, conn.Claims, conn.Relationships)
	block, err := engine.BuildContextBlock(ctx, query.ContextBlockOptions{
		RunID:     input.RunID,
		MaxTokens: input.MaxTokens,
	})
	if err != nil {
		return mcpMemoryContextOutput{}, err
	}
	return mcpMemoryContextOutput{RunID: input.RunID, Context: block}, nil
}

// --- Agent memory-management tools (Refs #41) ---
//
// remember / forget / update / search_memory are the four primitives an
// LLM agent needs to keep its own working memory inside Mnemos. They
// wrap the existing claim / event / evidence + similarity-search
// primitives so an agent doesn't have to compose three lower-level
// MCP tools to register a single fact.

type mcpRememberInput struct {
	Text       string  `json:"text" jsonschema:"required,description=The fact to remember"`
	Kind       string  `json:"kind,omitempty" jsonschema:"description=Claim kind: fact (default), hypothesis, decision"`
	RunID      string  `json:"run_id" jsonschema:"required,description=Tenant scope; events are stamped with this for run_id-filtered recall"`
	ValidUntil string  `json:"valid_until,omitempty" jsonschema:"description=RFC3339 timestamp at which this claim should automatically be considered no longer in force"`
	Confidence float64 `json:"confidence,omitempty" jsonschema:"description=Confidence in [0,1]; defaults to 0.9 when omitted"`
}

type mcpRememberOutput struct {
	ClaimID string `json:"claim_id"`
	EventID string `json:"event_id"`
	RunID   string `json:"run_id"`
	Status  string `json:"status"`
}

type mcpForgetInput struct {
	ClaimID string `json:"claim_id" jsonschema:"required,description=ID of the claim to forget (status flips to deprecated; audit history preserved)"`
	Reason  string `json:"reason,omitempty" jsonschema:"description=Why the agent is forgetting this claim (recorded in the audit trail)"`
}

type mcpForgetOutput struct {
	ClaimID   string `json:"claim_id"`
	OldStatus string `json:"old_status"`
	NewStatus string `json:"new_status"`
}

type mcpUpdateInput struct {
	ClaimID    string  `json:"claim_id" jsonschema:"required,description=ID of the claim to update"`
	NewText    string  `json:"new_text" jsonschema:"required,description=Replacement text for the claim"`
	Confidence float64 `json:"confidence,omitempty" jsonschema:"description=New confidence in [0,1]; omit to keep the current value"`
	Reason     string  `json:"reason,omitempty" jsonschema:"description=Why the agent is rewriting this claim (recorded in the audit trail)"`
}

type mcpUpdateOutput struct {
	ClaimID string `json:"claim_id"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

type mcpSearchMemoryInput struct {
	Query         string  `json:"query" jsonschema:"required,description=Free-text query; embedded by the configured provider and matched by cosine similarity"`
	RunID         string  `json:"run_id" jsonschema:"required,description=Tenant scope; required to prevent cross-tenant semantic leakage"`
	TopK          int     `json:"top_k,omitempty" jsonschema:"description=Maximum hits to return (default 10, max 100)"`
	MinSimilarity float64 `json:"min_similarity,omitempty" jsonschema:"description=Drop hits below this cosine similarity threshold (default 0)"`
}

type mcpSearchMemoryHit struct {
	ClaimID    string  `json:"claim_id"`
	Text       string  `json:"text"`
	Similarity float64 `json:"similarity"`
	Status     string  `json:"status"`
}

type mcpSearchMemoryOutput struct {
	Hits []mcpSearchMemoryHit `json:"hits"`
}

func mcpRunRemember(ctx context.Context, actor string, input mcpRememberInput) (mcpRememberOutput, error) {
	text := strings.TrimSpace(input.Text)
	runID := strings.TrimSpace(input.RunID)
	if text == "" {
		return mcpRememberOutput{}, fmt.Errorf("text is required")
	}
	if runID == "" {
		return mcpRememberOutput{}, fmt.Errorf("run_id is required")
	}
	kind := domain.ClaimType(strings.TrimSpace(input.Kind))
	if kind == "" {
		kind = domain.ClaimTypeFact
	}
	switch kind {
	case domain.ClaimTypeFact, domain.ClaimTypeHypothesis, domain.ClaimTypeDecision:
	default:
		return mcpRememberOutput{}, fmt.Errorf("kind must be one of: fact, hypothesis, decision (got %q)", input.Kind)
	}
	conf := input.Confidence
	if conf == 0 {
		conf = 0.9
	}
	if conf < 0 || conf > 1 {
		return mcpRememberOutput{}, fmt.Errorf("confidence must be in [0, 1]")
	}
	var validTo time.Time
	if raw := strings.TrimSpace(input.ValidUntil); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return mcpRememberOutput{}, fmt.Errorf("valid_until must be RFC3339: %v", err)
		}
		validTo = t.UTC()
	}

	conn, err := openConn(ctx)
	if err != nil {
		return mcpRememberOutput{}, err
	}
	defer closeConn(conn)

	now := time.Now().UTC()
	eventID := fmt.Sprintf("ev_remember_%d", now.UnixNano())
	claimID := fmt.Sprintf("cl_remember_%d", now.UnixNano())

	event := domain.Event{
		ID:            eventID,
		RunID:         runID,
		SchemaVersion: "v1",
		Content:       text,
		SourceInputID: "agent_remember",
		Timestamp:     now,
		IngestedAt:    now,
		CreatedBy:     actor,
	}
	if err := conn.Events.Append(ctx, event); err != nil {
		return mcpRememberOutput{}, fmt.Errorf("append event: %w", err)
	}

	claim := domain.Claim{
		ID:         claimID,
		Text:       text,
		Type:       kind,
		Confidence: conf,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  now,
		CreatedBy:  actor,
		ValidFrom:  now,
		ValidTo:    validTo,
	}
	reason := "agent-remember"
	if err := conn.Claims.UpsertWithReasonAs(ctx, []domain.Claim{claim}, reason, actor); err != nil {
		return mcpRememberOutput{}, fmt.Errorf("append claim: %w", err)
	}
	// ValidTo lives on a separate write path (SetValidity) because
	// it's mutated by lifecycle events (resolve --supersedes) rather
	// than on every upsert. Apply it post-insert when the caller set
	// an explicit TTL.
	if !validTo.IsZero() {
		if err := conn.Claims.SetValidity(ctx, claimID, validTo); err != nil {
			return mcpRememberOutput{}, fmt.Errorf("set valid_to: %w", err)
		}
	}
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
		{ClaimID: claimID, EventID: eventID},
	}); err != nil {
		return mcpRememberOutput{}, fmt.Errorf("link evidence: %w", err)
	}
	return mcpRememberOutput{
		ClaimID: claimID,
		EventID: eventID,
		RunID:   runID,
		Status:  string(domain.ClaimStatusActive),
	}, nil
}

func mcpRunForget(ctx context.Context, actor string, input mcpForgetInput) (mcpForgetOutput, error) {
	if strings.TrimSpace(input.ClaimID) == "" {
		return mcpForgetOutput{}, fmt.Errorf("claim_id is required")
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpForgetOutput{}, err
	}
	defer closeConn(conn)
	existing, err := conn.Claims.ListByIDs(ctx, []string{input.ClaimID})
	if err != nil || len(existing) == 0 {
		return mcpForgetOutput{}, fmt.Errorf("claim %s not found", input.ClaimID)
	}
	old := existing[0].Status
	updated := existing[0]
	updated.Status = domain.ClaimStatusDeprecated
	reason := input.Reason
	if reason == "" {
		reason = "agent-forget"
	}
	if err := conn.Claims.UpsertWithReasonAs(ctx, []domain.Claim{updated}, reason, actor); err != nil {
		return mcpForgetOutput{}, err
	}
	return mcpForgetOutput{
		ClaimID:   input.ClaimID,
		OldStatus: string(old),
		NewStatus: string(domain.ClaimStatusDeprecated),
	}, nil
}

func mcpRunUpdate(ctx context.Context, actor string, input mcpUpdateInput) (mcpUpdateOutput, error) {
	claimID := strings.TrimSpace(input.ClaimID)
	newText := strings.TrimSpace(input.NewText)
	if claimID == "" {
		return mcpUpdateOutput{}, fmt.Errorf("claim_id is required")
	}
	if newText == "" {
		return mcpUpdateOutput{}, fmt.Errorf("new_text is required")
	}
	if input.Confidence < 0 || input.Confidence > 1 {
		return mcpUpdateOutput{}, fmt.Errorf("confidence must be in [0, 1]")
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpUpdateOutput{}, err
	}
	defer closeConn(conn)
	existing, err := conn.Claims.ListByIDs(ctx, []string{claimID})
	if err != nil || len(existing) == 0 {
		return mcpUpdateOutput{}, fmt.Errorf("claim %s not found", claimID)
	}
	oldText := existing[0].Text
	updated := existing[0]
	updated.Text = newText
	if input.Confidence > 0 {
		updated.Confidence = input.Confidence
	}
	reason := input.Reason
	if reason == "" {
		reason = "agent-update"
	}
	if err := conn.Claims.UpsertWithReasonAs(ctx, []domain.Claim{updated}, reason, actor); err != nil {
		return mcpUpdateOutput{}, err
	}
	return mcpUpdateOutput{
		ClaimID: claimID,
		OldText: oldText,
		NewText: newText,
	}, nil
}

func mcpRunSearchMemory(ctx context.Context, input mcpSearchMemoryInput) (mcpSearchMemoryOutput, error) {
	q := strings.TrimSpace(input.Query)
	runID := strings.TrimSpace(input.RunID)
	if q == "" {
		return mcpSearchMemoryOutput{}, fmt.Errorf("query is required")
	}
	if runID == "" {
		return mcpSearchMemoryOutput{}, fmt.Errorf("run_id is required (tenant boundary)")
	}
	topK := input.TopK
	if topK == 0 {
		topK = 10
	}
	if topK < 0 || topK > 100 {
		return mcpSearchMemoryOutput{}, fmt.Errorf("top_k must be in [1, 100]")
	}
	if input.MinSimilarity < 0 || input.MinSimilarity > 1 {
		return mcpSearchMemoryOutput{}, fmt.Errorf("min_similarity must be in [0, 1]")
	}

	conn, err := openConn(ctx)
	if err != nil {
		return mcpSearchMemoryOutput{}, err
	}
	defer closeConn(conn)

	searcher, ok := conn.Embeddings.(ports.ClaimSimilaritySearcher)
	if !ok {
		return mcpSearchMemoryOutput{}, fmt.Errorf("current storage backend does not support vector similarity search")
	}
	embedder, err := embedderResolver()
	if err != nil {
		return mcpSearchMemoryOutput{}, fmt.Errorf("embedding provider not configured: %w", err)
	}

	events, err := conn.Events.ListByRunID(ctx, runID)
	if err != nil {
		return mcpSearchMemoryOutput{}, fmt.Errorf("list events for run: %w", err)
	}
	if len(events) == 0 {
		return mcpSearchMemoryOutput{Hits: []mcpSearchMemoryHit{}}, nil
	}
	allowedEventIDs := make(map[string]struct{}, len(events))
	for _, e := range events {
		allowedEventIDs[e.ID] = struct{}{}
	}
	allEvidence, err := conn.Claims.ListAllEvidence(ctx)
	if err != nil {
		return mcpSearchMemoryOutput{}, fmt.Errorf("list evidence: %w", err)
	}
	candidateClaimIDs := make(map[string]struct{})
	for _, link := range allEvidence {
		if _, ok := allowedEventIDs[link.EventID]; ok {
			candidateClaimIDs[link.ClaimID] = struct{}{}
		}
	}
	if len(candidateClaimIDs) == 0 {
		return mcpSearchMemoryOutput{Hits: []mcpSearchMemoryHit{}}, nil
	}

	vectors, err := embedder.Embed(ctx, []string{q})
	if err != nil {
		return mcpSearchMemoryOutput{}, fmt.Errorf("embed query: %w", err)
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return mcpSearchMemoryOutput{}, fmt.Errorf("embed query: provider returned empty vector")
	}

	hits, err := searcher.SearchClaimsByVector(ctx, vectors[0], candidateClaimIDs, topK, input.MinSimilarity)
	if err != nil {
		return mcpSearchMemoryOutput{}, fmt.Errorf("similarity search: %w", err)
	}
	if len(hits) == 0 {
		return mcpSearchMemoryOutput{Hits: []mcpSearchMemoryHit{}}, nil
	}

	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.ClaimID)
	}
	claims, err := conn.Claims.ListByIDs(ctx, ids)
	if err != nil {
		return mcpSearchMemoryOutput{}, fmt.Errorf("load claims: %w", err)
	}
	claimByID := make(map[string]domain.Claim, len(claims))
	for _, c := range claims {
		claimByID[c.ID] = c
	}
	out := make([]mcpSearchMemoryHit, 0, len(hits))
	for _, h := range hits {
		c, ok := claimByID[h.ClaimID]
		if !ok {
			continue
		}
		out = append(out, mcpSearchMemoryHit{
			ClaimID:    c.ID,
			Text:       c.Text,
			Similarity: h.Similarity,
			Status:     string(c.Status),
		})
	}
	return mcpSearchMemoryOutput{Hits: out}, nil
}
