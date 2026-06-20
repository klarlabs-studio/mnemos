package kernel_test

import (
	"context"
	"os"
	"testing"
	"time"

	"go.klarlabs.de/axi"
	"go.klarlabs.de/axi/domain"
	"go.klarlabs.de/bolt"
	"go.klarlabs.de/mnemos/internal/kernel"
)

func TestActionName_PrefixesAndDashes(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"remember":       "mnemos-remember",
		"remember_claim": "mnemos-remember-claim",
		"remember_event": "mnemos-remember-event",
	}
	for in, want := range cases {
		if got := kernel.ActionName(in); got != want {
			t.Errorf("ActionName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExecutorRef(t *testing.T) {
	t.Parallel()
	if got := kernel.ExecutorRef("remember"); got != "exec.remember" {
		t.Errorf("ExecutorRef = %q, want exec.remember", got)
	}
}

// stubExecutor returns a fixed evidence record so tests can drive the
// kernel without touching storage.
type stubExecutor struct {
	tokens int64
}

func (e stubExecutor) Execute(_ context.Context, _ any, _ domain.CapabilityInvoker) (domain.ExecutionResult, []domain.EvidenceRecord, error) {
	recs := []domain.EvidenceRecord{
		{Kind: "stub", Source: "test", Value: map[string]any{"ok": true}},
	}
	if e.tokens > 0 {
		recs = append(recs, domain.EvidenceRecord{Kind: "llm", Source: "test", TokensUsed: e.tokens})
	}
	return domain.ExecutionResult{Data: map[string]any{"ok": true}, Summary: "stub ran"}, recs, nil
}

func testActions() []kernel.Action {
	return []kernel.Action{
		{Name: "remember", Effect: domain.EffectWriteLocal, Idempotent: false, Description: "remember"},
		{Name: "remember_event", Effect: domain.EffectWriteLocal, Idempotent: true, Description: "remember event"},
	}
}

func TestBuild_RegistersAllActions(t *testing.T) {
	t.Parallel()
	execs := map[string]domain.ActionExecutor{
		kernel.ExecutorRef("remember"):       stubExecutor{},
		kernel.ExecutorRef("remember_event"): stubExecutor{},
	}
	k, err := kernel.Build(nil, testActions(), execs, axi.Budget{}, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := k.ListActions()
	if len(got) != len(testActions()) {
		t.Fatalf("registered %d actions, want %d", len(got), len(testActions()))
	}
	want := map[string]domain.EffectLevel{
		kernel.ActionName("remember"):       domain.EffectWriteLocal,
		kernel.ActionName("remember_event"): domain.EffectWriteLocal,
	}
	for _, a := range got {
		lvl, ok := want[string(a.Name())]
		if !ok {
			t.Errorf("unexpected action %s", a.Name())
			continue
		}
		if a.EffectProfile().Level != lvl {
			t.Errorf("%s effect = %s, want %s", a.Name(), a.EffectProfile().Level, lvl)
		}
	}
}

// TestBuild_WriteProducesVerifiableEvidenceChain proves a write-local
// action through the built kernel records a session whose evidence chain
// verifies and is populated.
func TestBuild_WriteProducesVerifiableEvidenceChain(t *testing.T) {
	t.Parallel()
	execs := map[string]domain.ActionExecutor{
		kernel.ExecutorRef("remember"): stubExecutor{},
	}
	k, err := kernel.Build(nil, testActions(), execs, axi.Budget{}, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res, err := k.Execute(context.Background(), axi.Invocation{Action: kernel.ActionName("remember")})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Failure != nil {
		t.Fatalf("execution failed: %s — %s", res.Failure.Code, res.Failure.Message)
	}
	session, err := k.GetSession(string(res.SessionID))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if err := session.VerifyEvidenceChain(); err != nil {
		t.Errorf("evidence chain broken: %v", err)
	}
	if len(session.Evidence()) == 0 {
		t.Error("expected at least one evidence record")
	}
}

// TestBuild_TokenBudgetBlocksWrite proves a per-session token budget
// below the reported spend fails the write with a budget error.
func TestBuild_TokenBudgetBlocksWrite(t *testing.T) {
	t.Parallel()
	execs := map[string]domain.ActionExecutor{
		kernel.ExecutorRef("remember"): stubExecutor{tokens: 500},
	}
	k, err := kernel.Build(nil, testActions(), execs, axi.Budget{MaxTokens: 100}, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res, err := k.Execute(context.Background(), axi.Invocation{Action: kernel.ActionName("remember")})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Failure == nil {
		t.Fatal("expected budget failure, got success")
	}
	if res.Failure.Code != "BUDGET_EXCEEDED" {
		t.Errorf("failure code = %q, want BUDGET_EXCEEDED", res.Failure.Code)
	}
}

func TestBudgetFromEnv_Defaults(t *testing.T) {
	t.Setenv("MNEMOS_AXI_MAX_DURATION", "")
	t.Setenv("MNEMOS_AXI_MAX_INVOCATIONS", "")
	t.Setenv("MNEMOS_AXI_MAX_TOKENS", "")
	b := kernel.BudgetFromEnv()
	if b.MaxTokens != 0 {
		t.Errorf("default MaxTokens = %d, want 0 (unlimited)", b.MaxTokens)
	}
	if b.MaxCapabilityInvocations != 1000 {
		t.Errorf("default MaxCapabilityInvocations = %d, want 1000", b.MaxCapabilityInvocations)
	}
}

func TestBudgetFromEnv_Overrides(t *testing.T) {
	t.Setenv("MNEMOS_AXI_MAX_TOKENS", "42")
	t.Setenv("MNEMOS_AXI_MAX_INVOCATIONS", "7")
	t.Setenv("MNEMOS_AXI_MAX_DURATION", "30s")
	b := kernel.BudgetFromEnv()
	if b.MaxTokens != 42 {
		t.Errorf("MaxTokens = %d, want 42", b.MaxTokens)
	}
	if b.MaxCapabilityInvocations != 7 {
		t.Errorf("MaxCapabilityInvocations = %d, want 7", b.MaxCapabilityInvocations)
	}
	if b.MaxDuration != 30*time.Second {
		t.Errorf("MaxDuration = %s, want 30s", b.MaxDuration)
	}
}

// TestBudgetFromEnv_InvalidValuesIgnored confirms malformed env values
// fall back to the permissive defaults rather than erroring.
func TestBudgetFromEnv_InvalidValuesIgnored(t *testing.T) {
	t.Setenv("MNEMOS_AXI_MAX_DURATION", "not-a-duration")
	t.Setenv("MNEMOS_AXI_MAX_INVOCATIONS", "-5")
	t.Setenv("MNEMOS_AXI_MAX_TOKENS", "abc")
	b := kernel.BudgetFromEnv()
	if b.MaxDuration != 5*time.Minute {
		t.Errorf("MaxDuration = %s, want default 5m", b.MaxDuration)
	}
	if b.MaxCapabilityInvocations != 1000 {
		t.Errorf("MaxCapabilityInvocations = %d, want default 1000", b.MaxCapabilityInvocations)
	}
	if b.MaxTokens != 0 {
		t.Errorf("MaxTokens = %d, want default 0", b.MaxTokens)
	}
}

func TestMain(m *testing.M) {
	// Keep axi chatter off stderr for the package's own tests.
	_ = bolt.New(bolt.NewJSONHandler(os.Stderr))
	os.Exit(m.Run())
}
