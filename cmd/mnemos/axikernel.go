package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/felixgeelhaar/axi-go"
	"github.com/felixgeelhaar/axi-go/domain"
	"github.com/felixgeelhaar/bolt"
)

// axiActionPrefix is the axi-go action name corresponding to each MCP
// tool. axi-go's name regex disallows underscores, so we map at the
// boundary: agents see `process_text` (MCP), the kernel sees
// `process-text`. The MCP protocol surface stays unchanged.
const axiActionPrefix = "mnemos-mcp-"

// mcpTools returns the descriptors for every MCP-exposed Mnemos tool.
// One source of truth so the kernel registration and the MCP handler
// dispatch can't drift.
func mcpTools() []mcpToolDescriptor {
	return []mcpToolDescriptor{
		{Name: "query_knowledge", Effect: domain.EffectReadLocal, Idempotent: true,
			Description: "Query the Mnemos knowledge base and return evidence-backed results."},
		{Name: "process_text", Effect: domain.EffectWriteLocal, Idempotent: false,
			Description: "Ingest raw text, extract claims, detect relationships, and optionally generate embeddings."},
		{Name: "knowledge_metrics", Effect: domain.EffectReadLocal, Idempotent: true,
			Description: "Return counts and statistics about the Mnemos knowledge base."},
		{Name: "list_claims", Effect: domain.EffectReadLocal, Idempotent: true,
			Description: "List claims with optional type/status filtering and pagination."},
		{Name: "list_decisions", Effect: domain.EffectReadLocal, Idempotent: true,
			Description: "List claims classified as decisions."},
		{Name: "list_contradictions", Effect: domain.EffectReadLocal, Idempotent: true,
			Description: "List contradiction relationships hydrated with both claims' text."},
		{Name: "ingest_git_log", Effect: domain.EffectWriteLocal, Idempotent: true,
			Description: "Ingest recent git commits from the project repository as events."},
		{Name: "ingest_git_prs", Effect: domain.EffectWriteLocal, Idempotent: true,
			Description: "Ingest merged GitHub pull requests as events. Requires gh CLI authentication."},
		{Name: "watch_file", Effect: domain.EffectWriteLocal, Idempotent: true,
			Description: "Register a file to be re-ingested when its content changes."},
	}
}

type mcpToolDescriptor struct {
	Name        string
	Effect      domain.EffectLevel
	Idempotent  bool
	Description string
}

// axiActionName returns the axi-go-compatible action name for the
// given MCP tool name. Centralising the conversion keeps the regex
// dependency in one place.
func axiActionName(mcpName string) string {
	return axiActionPrefix + strings.ReplaceAll(mcpName, "_", "-")
}

// buildMCPKernel builds an axi-go kernel pre-registered with every
// MCP tool. Executors are passed in by the caller because each tool's
// implementation has different shape — the kernel just provides the
// uniform execution surface (effect gating, evidence chain, budget).
//
// The returned kernel carries the bolt logger and a publisher that
// forwards every domain event to the same logger. Token/duration
// budgets are read from MNEMOS_AXI_MAX_DURATION and
// MNEMOS_AXI_MAX_INVOCATIONS; both default to permissive caps so
// adoption doesn't break existing flows.
func buildMCPKernel(logger *bolt.Logger, executors map[string]domain.ActionExecutor) (*axi.Kernel, error) {
	plugin, err := newMnemosMCPPlugin()
	if err != nil {
		return nil, fmt.Errorf("build plugin: %w", err)
	}

	// Compose two publishers: bolt (line-protocol log every event) +
	// optional JSONL sink (cross-session audit chain on disk; opt-in
	// via MNEMOS_AXI_EVIDENCE_LOG). The multiplex publisher routes
	// every event to both.
	publishers := []domain.DomainEventPublisher{
		boltAxiPublisher{logger: logger},
	}
	evidenceSink, evidenceErr := newAxiEvidenceSink("")
	if evidenceErr != nil {
		logger.Warn().Err(evidenceErr).Msg("axi evidence sink disabled")
	} else if evidenceSink != nil {
		publishers = append(publishers, evidenceSink)
	}

	kernel := axi.New().
		WithLogger(boltAxiLogger{logger: logger}).
		WithDomainEventPublisher(multiAxiPublisher(publishers)).
		WithBudget(axiBudgetFromEnv())

	for ref, exec := range executors {
		kernel.RegisterActionExecutor(ref, exec)
	}
	if err := kernel.RegisterPlugin(plugin); err != nil {
		return nil, fmt.Errorf("register plugin: %w", err)
	}
	return kernel, nil
}

// mnemosMCPPlugin is the static plugin describing every MCP tool as
// an axi-go action. It contributes definitions only — executors are
// registered separately so they can carry runtime state (db handles,
// resolved actor, etc.) without leaking into the static metadata.
type mnemosMCPPlugin struct {
	actions []*domain.ActionDefinition
}

// Contribute satisfies domain.Plugin.
func (p mnemosMCPPlugin) Contribute() (*domain.PluginContribution, error) {
	return domain.NewPluginContribution("mnemos.mcp", p.actions, nil)
}

func newMnemosMCPPlugin() (mnemosMCPPlugin, error) {
	p := mnemosMCPPlugin{}
	for _, t := range mcpTools() {
		name, err := domain.NewActionName(axiActionName(t.Name))
		if err != nil {
			return p, fmt.Errorf("invalid axi action name for %s: %w", t.Name, err)
		}
		action, err := domain.NewActionDefinition(
			name,
			t.Description,
			domain.EmptyContract(), // typed validation happens at the MCP layer
			domain.EmptyContract(),
			nil,
			domain.EffectProfile{Level: t.Effect},
			domain.IdempotencyProfile{IsIdempotent: t.Idempotent},
		)
		if err != nil {
			return p, fmt.Errorf("define action %s: %w", t.Name, err)
		}
		if err := action.BindExecutor(domain.ActionExecutorRef("exec." + t.Name)); err != nil {
			return p, fmt.Errorf("bind executor for %s: %w", t.Name, err)
		}
		p.actions = append(p.actions, action)
	}
	return p, nil
}

// axiBudgetFromEnv reads MNEMOS_AXI_MAX_DURATION (Go duration string)
// and MNEMOS_AXI_MAX_INVOCATIONS (positive int) and returns the
// resulting budget. Unset / invalid values fall back to permissive
// defaults: 5 minutes / 1000 invocations. Token budgets are not
// enforced here yet — the LLM client doesn't report TokensUsed
// through capabilities (would land in a follow-on slice).
func axiBudgetFromEnv() axi.Budget {
	b := axi.Budget{
		MaxDuration:              5 * time.Minute,
		MaxCapabilityInvocations: 1000,
	}
	if v := os.Getenv("MNEMOS_AXI_MAX_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			b.MaxDuration = d
		}
	}
	if v := os.Getenv("MNEMOS_AXI_MAX_INVOCATIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			b.MaxCapabilityInvocations = n
		}
	}
	return b
}

// boltAxiLogger adapts our existing *bolt.Logger to axi-go's
// domain.Logger interface so the kernel's internal logging lands in
// the same JSON stream as the MCP server's own access log.
type boltAxiLogger struct{ logger *bolt.Logger }

func (l boltAxiLogger) Debug(msg string, fields ...domain.Field) {
	l.emit(l.logger.Debug(), msg, fields)
}
func (l boltAxiLogger) Info(msg string, fields ...domain.Field) {
	l.emit(l.logger.Info(), msg, fields)
}
func (l boltAxiLogger) Warn(msg string, fields ...domain.Field) {
	l.emit(l.logger.Warn(), msg, fields)
}
func (l boltAxiLogger) Error(msg string, fields ...domain.Field) {
	l.emit(l.logger.Error(), msg, fields)
}

func (l boltAxiLogger) emit(ev *bolt.Event, msg string, fields []domain.Field) {
	for _, f := range fields {
		ev = ev.Any(f.Key, f.Value)
	}
	ev.Msg(msg)
}

// boltAxiPublisher fans every kernel domain event into bolt as a
// structured "axi_event" log line. Cheap, synchronous, never blocks
// the execution path beyond a single JSON encode.
type boltAxiPublisher struct{ logger *bolt.Logger }

func (p boltAxiPublisher) Publish(e domain.DomainEvent) {
	p.logger.Info().
		Str("event_type", e.EventType()).
		Str("occurred_at", e.OccurredAt().UTC().Format(time.RFC3339Nano)).
		Msg("axi_event")
}

// remarshal converts between two arbitrary JSON-shaped types via a
// JSON round-trip. Used to bridge axi-go's untyped `any` inputs
// (map[string]any from agents) into our typed mcpXxxInput structs and
// back. Slow on the hot path? Each MCP call is bounded by network
// latency and DB writes — JSON round-trips are rounding error.
func remarshal(in any, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	return nil
}

// toInputMap normalises an arbitrary typed input into the
// map[string]any shape axi-go expects on Invocation.Input. Tools that
// take a `struct{}` (no-arg) end up with an empty map; named fields
// preserve their JSON tags via the round-trip.
func toInputMap(in any) (map[string]any, error) {
	if in == nil {
		return map[string]any{}, nil
	}
	if m, ok := in.(map[string]any); ok {
		return m, nil
	}
	out := map[string]any{}
	if err := remarshal(in, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// mcpExecutorMap returns the canonical executor binding for every
// MCP tool. A single map keyed by axi-go executor ref keeps the
// "what tool dispatches where" answer in one place.
//
// Each executor closure captures the actor and (if it needs DB
// access) opens a per-call connection or reuses a shared *sql.DB.
// Today Mnemos opens fresh connections inside each handler, so the
// closures defer to the existing mcpRunXxx helpers without surfacing
// a *sql.DB here.
func mcpExecutorMap(actor string, getWatcher func() (*Watcher, error)) map[string]domain.ActionExecutor {
	return map[string]domain.ActionExecutor{
		"exec.query_knowledge": toolExecutor[mcpQueryInput, mcpQueryOutput]{run: func(ctx context.Context, in mcpQueryInput) (mcpQueryOutput, error) { return mcpRunQuery(ctx, in) }, summary: querySummary},
		"exec.process_text": toolExecutor[mcpProcessTextInput, mcpProcessTextOutput]{run: func(ctx context.Context, in mcpProcessTextInput) (mcpProcessTextOutput, error) {
			return mcpRunProcessText(ctx, actor, in)
		}, summary: processTextSummary, evidence: processTextEvidence},
		"exec.knowledge_metrics": toolExecutor[struct{}, mcpMetricsOutput]{run: func(_ context.Context, _ struct{}) (mcpMetricsOutput, error) { return mcpRunMetrics() }, summary: metricsSummary},
		"exec.list_claims": toolExecutor[mcpListClaimsInput, mcpListClaimsOutput]{run: func(ctx context.Context, in mcpListClaimsInput) (mcpListClaimsOutput, error) {
			return mcpRunListClaims(ctx, in)
		}, summary: listClaimsSummary},
		"exec.list_decisions": toolExecutor[mcpListClaimsInput, mcpListClaimsOutput]{run: func(ctx context.Context, in mcpListClaimsInput) (mcpListClaimsOutput, error) {
			in.Type = "decision"
			return mcpRunListClaims(ctx, in)
		}, summary: listClaimsSummary},
		"exec.list_contradictions": toolExecutor[mcpListContradictionsInput, mcpListContradictionsOutput]{run: func(ctx context.Context, in mcpListContradictionsInput) (mcpListContradictionsOutput, error) {
			return mcpRunListContradictions(ctx, in)
		}, summary: listContradictionsSummary},
		"exec.ingest_git_log": toolExecutor[mcpIngestGitLogInput, mcpIngestGitLogOutput]{run: func(ctx context.Context, in mcpIngestGitLogInput) (mcpIngestGitLogOutput, error) {
			return mcpRunIngestGitLog(ctx, actor, in)
		}, summary: ingestGitLogSummary, evidence: ingestGitLogEvidence},
		"exec.ingest_git_prs": toolExecutor[mcpIngestGitPRsInput, mcpIngestGitPRsOutput]{run: func(ctx context.Context, in mcpIngestGitPRsInput) (mcpIngestGitPRsOutput, error) {
			return mcpRunIngestGitPRs(ctx, actor, in)
		}, summary: ingestGitPRsSummary, evidence: ingestGitPRsEvidence},
		"exec.watch_file": toolExecutor[mcpWatchFileInput, mcpWatchFileOutput]{run: func(_ context.Context, in mcpWatchFileInput) (mcpWatchFileOutput, error) {
			return runWatchFileTool(in, getWatcher)
		}, summary: watchFileSummary},
	}
}

// toolExecutor is the generic adapter that lifts a `func(ctx, In)
// (Out, error)` into a domain.ActionExecutor. Eliminates per-tool
// boilerplate by handling the input/output JSON round-trip and
// summary/evidence projection in one place.
type toolExecutor[In any, Out any] struct {
	run      func(context.Context, In) (Out, error)
	summary  func(Out) string
	evidence func(Out) []domain.EvidenceRecord
}

// Execute satisfies domain.ActionExecutor.
func (e toolExecutor[In, Out]) Execute(ctx context.Context, input any, _ domain.CapabilityInvoker) (domain.ExecutionResult, []domain.EvidenceRecord, error) {
	var typed In
	if input != nil {
		if err := remarshal(input, &typed); err != nil {
			return domain.ExecutionResult{}, nil, err
		}
	}
	out, err := e.run(ctx, typed)
	if err != nil {
		return domain.ExecutionResult{}, nil, err
	}
	res := domain.ExecutionResult{Data: out}
	if e.summary != nil {
		res.Summary = e.summary(out)
	}
	var evs []domain.EvidenceRecord
	if e.evidence != nil {
		evs = e.evidence(out)
	}
	return res, evs, nil
}

// runWatchFileTool wraps the existing watch_file MCP handler shape so
// the executor closure stays one-liner-shaped like the others.
func runWatchFileTool(in mcpWatchFileInput, getWatcher func() (*Watcher, error)) (mcpWatchFileOutput, error) {
	w, err := getWatcher()
	if err != nil {
		return mcpWatchFileOutput{}, err
	}
	count, err := w.Add(in.Path)
	if err != nil {
		return mcpWatchFileOutput{}, err
	}
	return mcpWatchFileOutput{Watching: true, Path: in.Path, ActiveWatches: count}, nil
}

// --- per-tool summary + evidence projections ---
//
// Summaries are short single-line agent-readable hints. Evidence
// records carry the structured facts that should land in the
// tamper-evident chain: counts, IDs, anything an auditor would want
// to verify after the fact.

func querySummary(out mcpQueryOutput) string {
	return fmt.Sprintf("answered with %d claim(s), %d contradiction(s)", len(out.Claims), len(out.Contradictions))
}

func processTextSummary(out mcpProcessTextOutput) string {
	return fmt.Sprintf("ingested run=%s events=%d claims=%d relationships=%d", out.RunID, out.Events, out.Claims, out.Relationships)
}

func processTextEvidence(out mcpProcessTextOutput) []domain.EvidenceRecord {
	return []domain.EvidenceRecord{
		{Kind: "mnemos.process_text", Source: "mnemos.mcp", Value: map[string]any{
			"run_id":        out.RunID,
			"events":        out.Events,
			"claims":        out.Claims,
			"relationships": out.Relationships,
			"embeddings":    out.Embeddings,
			"used_llm":      out.UsedLLM,
			"used_embed":    out.UsedEmbeddings,
		}},
	}
}

func metricsSummary(out mcpMetricsOutput) string {
	return fmt.Sprintf("runs=%d events=%d claims=%d contradictions=%d", out.Runs, out.Events, out.Claims, out.Contradictions)
}

func listClaimsSummary(out mcpListClaimsOutput) string {
	return fmt.Sprintf("listed %d/%d claim(s)", len(out.Claims), out.Total)
}

func listContradictionsSummary(out mcpListContradictionsOutput) string {
	return fmt.Sprintf("listed %d/%d contradiction(s)", len(out.Contradictions), out.Total)
}

func ingestGitLogSummary(out mcpIngestGitLogOutput) string {
	return fmt.Sprintf("git_log: ingested=%d skipped=%d", out.Ingested, out.Skipped)
}

func ingestGitLogEvidence(out mcpIngestGitLogOutput) []domain.EvidenceRecord {
	return []domain.EvidenceRecord{
		{Kind: "mnemos.ingest.git_log", Source: "mnemos.mcp", Value: map[string]any{
			"ingested": out.Ingested, "skipped": out.Skipped,
		}},
	}
}

func ingestGitPRsSummary(out mcpIngestGitPRsOutput) string {
	return fmt.Sprintf("git_prs: ingested=%d skipped=%d", out.Ingested, out.Skipped)
}

func ingestGitPRsEvidence(out mcpIngestGitPRsOutput) []domain.EvidenceRecord {
	return []domain.EvidenceRecord{
		{Kind: "mnemos.ingest.git_prs", Source: "mnemos.mcp", Value: map[string]any{
			"ingested": out.Ingested, "skipped": out.Skipped,
		}},
	}
}

func watchFileSummary(out mcpWatchFileOutput) string {
	return fmt.Sprintf("watching %s (active=%d)", out.Path, out.ActiveWatches)
}

// dispatchAxiTool runs an MCP tool through the kernel and decodes the
// result back into the typed Out shape. The boilerplate is identical
// across every tool, so isolating it here keeps the MCP file readable.
//
// The unused db parameter is reserved: future enrichment (auditing
// invocations into a mcp_invocations table from the same DB) will
// thread it through without changing call sites.
func dispatchAxiTool[Out any](ctx context.Context, kernel *axi.Kernel, _ *sql.DB, mcpName string, input any) (Out, error) {
	var zero Out
	payload, err := toInputMap(input)
	if err != nil {
		return zero, fmt.Errorf("encode %s input: %w", mcpName, err)
	}
	res, err := kernel.Execute(ctx, axi.Invocation{
		Action: axiActionName(mcpName),
		Input:  payload,
	})
	if err != nil {
		return zero, err
	}
	if res.Failure != nil {
		return zero, fmt.Errorf("%s: %s", res.Failure.Code, res.Failure.Message)
	}
	if res.Result == nil {
		return zero, fmt.Errorf("%s: kernel returned no result", mcpName)
	}
	if typed, ok := res.Result.Data.(Out); ok {
		return typed, nil
	}
	if err := remarshal(res.Result.Data, &zero); err != nil {
		return zero, fmt.Errorf("decode %s result: %w", mcpName, err)
	}
	return zero, nil
}
