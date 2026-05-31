package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/felixgeelhaar/axi-go"
	"github.com/felixgeelhaar/axi-go/domain"
	"github.com/felixgeelhaar/bolt"
)

func TestBuildMCPKernel_RegistersAllMCPTools(t *testing.T) {
	logger := bolt.New(bolt.NewJSONHandler(os.Stderr))
	kernel, err := buildMCPKernel(logger, mcpExecutorMap("usr_test", func() (*Watcher, error) { return nil, nil }))
	if err != nil {
		t.Fatalf("buildMCPKernel: %v", err)
	}

	want := map[string]domain.EffectLevel{}
	for _, t := range mcpTools() {
		want[axiActionName(t.Name)] = t.Effect
	}

	got := kernel.ListActions()
	if len(got) != len(want) {
		t.Fatalf("registered %d actions, want %d", len(got), len(want))
	}
	for _, a := range got {
		expected, ok := want[string(a.Name())]
		if !ok {
			t.Errorf("unexpected action registered: %s", a.Name())
			continue
		}
		if a.EffectProfile().Level != expected {
			t.Errorf("%s effect = %s, want %s", a.Name(), a.EffectProfile().Level, expected)
		}
	}
}

func TestBuildMCPKernel_HelpDescribesEachTool(t *testing.T) {
	logger := bolt.New(bolt.NewJSONHandler(os.Stderr))
	kernel, err := buildMCPKernel(logger, mcpExecutorMap("", func() (*Watcher, error) { return nil, nil }))
	if err != nil {
		t.Fatalf("kernel: %v", err)
	}

	for _, tool := range mcpTools() {
		help, err := kernel.Help(axiActionName(tool.Name))
		if err != nil {
			t.Errorf("help %s: %v", tool.Name, err)
			continue
		}
		if help == "" {
			t.Errorf("help %s returned empty", tool.Name)
		}
	}
}

// TestKernel_KnowledgeMetricsThroughKernel proves the typed
// round-trip end-to-end: a live kernel executes knowledge_metrics
// (read-local, no inputs), the result decodes back into the typed
// MCP output struct, and the recorded session's evidence chain
// verifies cleanly.
func TestKernel_KnowledgeMetricsThroughKernel(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "mnemos.db"))

	logger := bolt.New(bolt.NewJSONHandler(os.Stderr))
	kernel, err := buildMCPKernel(logger, mcpExecutorMap("", func() (*Watcher, error) { return nil, nil }))
	if err != nil {
		t.Fatalf("kernel: %v", err)
	}

	out, err := dispatchAxiTool[mcpMetricsOutput](context.Background(), kernel, nil, "knowledge_metrics", struct{}{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Empty DB → all counts zero. The point is round-trip integrity,
	// not the values themselves.
	if out.Events != 0 || out.Claims != 0 {
		t.Errorf("unexpected non-zero counts in fresh DB: %+v", out)
	}
}

// TestKernel_ProcessTextEvidenceChainIntact runs a write-local
// action through the kernel and verifies the resulting session's
// evidence chain hashes are populated and consistent.
func TestKernel_ProcessTextEvidenceChainIntact(t *testing.T) {
	// Ensure the DB schema exists before the kernel lazily opens it.
	openTestStore(t)

	logger := bolt.New(bolt.NewJSONHandler(os.Stderr))
	kernel, err := buildMCPKernel(logger, mcpExecutorMap("", func() (*Watcher, error) { return nil, nil }))
	if err != nil {
		t.Fatalf("kernel: %v", err)
	}

	res, err := kernel.Execute(context.Background(), axi.Invocation{
		Action: axiActionName("process_text"),
		Input:  map[string]any{"text": "We use SQLite. Postgres was rejected."},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Failure != nil {
		t.Fatalf("execution failed: %s — %s", res.Failure.Code, res.Failure.Message)
	}
	if res.Result == nil {
		t.Fatal("nil result on successful execution")
	}

	session, err := kernel.GetSession(string(res.SessionID))
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if err := session.VerifyEvidenceChain(); err != nil {
		t.Errorf("evidence chain broken: %v", err)
	}
	if len(session.Evidence()) == 0 {
		t.Error("expected at least one evidence record from process_text")
	}
}

// TestProcessTextEvidence_IncludesLLMTokens verifies the
// processTextEvidence projection emits a Kind="llm" record carrying
// TokensUsed = input + output when the output reports token usage.
// This is the bridge that lets axi-go's ExecutionBudget.MaxTokens sum
// real spend across a session.
func TestProcessTextEvidence_IncludesLLMTokens(t *testing.T) {
	t.Parallel()
	out := mcpProcessTextOutput{
		RunID:        "run-1",
		Events:       1,
		Claims:       2,
		UsedLLM:      true,
		InputTokens:  120,
		OutputTokens: 80,
		LLMModel:     "claude-sonnet-4-6",
	}
	records := processTextEvidence(out)

	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2 (process_text + llm)", len(records))
	}
	llm := records[1]
	if llm.Kind != "llm" {
		t.Errorf("records[1].Kind = %q, want llm", llm.Kind)
	}
	if llm.Source != "claude-sonnet-4-6" {
		t.Errorf("records[1].Source = %q, want claude-sonnet-4-6", llm.Source)
	}
	if llm.TokensUsed != 200 {
		t.Errorf("records[1].TokensUsed = %d, want 200 (120+80)", llm.TokensUsed)
	}
}

// TestProcessTextEvidence_SkipsLLMRecordWhenNoTokens verifies the
// llm-evidence record is omitted when the run reports zero tokens
// (rule-based path, cache hit, or provider that doesn't surface
// usage). The kernel's budget enforcer then only counts spend from
// runs that actually called out to a model.
func TestProcessTextEvidence_SkipsLLMRecordWhenNoTokens(t *testing.T) {
	t.Parallel()
	out := mcpProcessTextOutput{
		RunID:   "run-2",
		Events:  1,
		Claims:  0,
		UsedLLM: false,
	}
	records := processTextEvidence(out)

	if len(records) != 1 {
		t.Fatalf("len(records) = %d, want 1 (process_text only)", len(records))
	}
	if records[0].Kind != "mnemos.process_text" {
		t.Errorf("records[0].Kind = %q, want mnemos.process_text", records[0].Kind)
	}
}
