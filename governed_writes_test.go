package mnemos_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/providers"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// newGovernedMemory builds a passive-mode Memory on an isolated
// in-memory namespace for the governed-write tests.
func newGovernedMemory(t *testing.T, ns string) mnemos.Memory {
	t.Helper()
	clearMnemosEnv(t)
	mem, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace="+ns),
		mnemos.WithPassiveMode(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	return mem
}

// TestRemember_RoutesThroughKernel verifies a Remember write produces a
// WriteSession whose evidence chain verifies and whose Evidence() is
// populated.
func TestRemember_RoutesThroughKernel(t *testing.T) {
	mem := newGovernedMemory(t, "gov_remember")
	ctx := context.Background()

	if mem.LastWriteSession() != nil {
		t.Fatal("expected no write session before any write")
	}

	if err := mem.Remember(ctx, mnemos.Item{
		Type:    "fact",
		Content: "We use SQLite. Postgres was rejected.",
		Source:  "test",
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	ws := mem.LastWriteSession()
	if ws == nil {
		t.Fatal("LastWriteSession is nil after Remember")
	}
	if ws.ID() == "" {
		t.Error("WriteSession.ID is empty")
	}
	if err := ws.VerifyEvidenceChain(); err != nil {
		t.Errorf("evidence chain broken: %v", err)
	}
	if len(ws.Evidence()) == 0 {
		t.Error("expected at least one evidence record from Remember")
	}
}

// TestRememberClaim_RoutesThroughKernel verifies RememberClaim produces
// a verifiable session and still returns the claim id.
func TestRememberClaim_RoutesThroughKernel(t *testing.T) {
	mem := newGovernedMemory(t, "gov_remember_claim")
	ctx := context.Background()

	id, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text: "The deploy pipeline runs on GitHub Actions.",
		Type: "fact",
	})
	if err != nil {
		t.Fatalf("RememberClaim: %v", err)
	}
	if id == "" {
		t.Fatal("RememberClaim returned empty id")
	}

	ws := mem.LastWriteSession()
	if ws == nil {
		t.Fatal("LastWriteSession is nil after RememberClaim")
	}
	if err := ws.VerifyEvidenceChain(); err != nil {
		t.Errorf("evidence chain broken: %v", err)
	}
	if len(ws.Evidence()) == 0 {
		t.Error("expected evidence record from RememberClaim")
	}
	// The claim id must appear in the evidence so an auditor can verify
	// the write after the fact.
	found := false
	for _, ev := range ws.Evidence() {
		if m, ok := ev.Value.(map[string]any); ok {
			if v, ok := m["claim_id"]; ok && v == id {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("claim id %q not found in evidence records", id)
	}
}

// TestRememberEvent_RoutesThroughKernel verifies RememberEvent produces
// a verifiable session.
func TestRememberEvent_RoutesThroughKernel(t *testing.T) {
	mem := newGovernedMemory(t, "gov_remember_event")
	ctx := context.Background()

	if err := mem.RememberEvent(ctx, mnemos.Event{
		ID:      "ev-gov-1",
		At:      time.Now(),
		Type:    "deployment",
		Content: "Deployed v1.2.3 to production.",
	}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}

	ws := mem.LastWriteSession()
	if ws == nil {
		t.Fatal("LastWriteSession is nil after RememberEvent")
	}
	if err := ws.VerifyEvidenceChain(); err != nil {
		t.Errorf("evidence chain broken: %v", err)
	}
	if len(ws.Evidence()) == 0 {
		t.Error("expected evidence record from RememberEvent")
	}
}

// TestWriteSession_CorruptedEvidenceFailsVerification confirms that
// tampering with a recorded evidence record makes VerifyEvidenceChain
// fail — proving the chain is genuinely tamper-evident.
func TestWriteSession_CorruptedEvidenceFailsVerification(t *testing.T) {
	mem := newGovernedMemory(t, "gov_corrupt")
	ctx := context.Background()

	if err := mem.RememberEvent(ctx, mnemos.Event{
		ID:      "ev-corrupt-1",
		At:      time.Now(),
		Type:    "incident",
		Content: "Disk filled on db-primary.",
	}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}

	ws := mem.LastWriteSession()
	if ws == nil {
		t.Fatal("LastWriteSession is nil")
	}
	if err := ws.VerifyEvidenceChain(); err != nil {
		t.Fatalf("baseline chain should verify: %v", err)
	}
	if len(ws.Evidence()) == 0 {
		t.Fatal("no evidence to corrupt")
	}

	// Tamper with the first record's Value while preserving its stored
	// hash; verification must recompute and detect the mismatch.
	if err := mnemos.TamperEvidenceForTest(ws, 0); err == nil {
		t.Error("expected verification to fail on tampered evidence")
	}
}

// TestNoBypass_EveryWriteLeavesASession asserts that none of the three
// public write paths can complete without recording a governance
// session — i.e. there is no direct-write fallback that bypasses the
// kernel.
func TestNoBypass_EveryWriteLeavesASession(t *testing.T) {
	mem := newGovernedMemory(t, "gov_no_bypass")
	ctx := context.Background()

	if err := mem.Remember(ctx, mnemos.Item{Type: "fact", Content: "Alpha fact."}); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	afterRemember := mem.LastWriteSession()
	if afterRemember == nil || afterRemember.ID() == "" {
		t.Fatal("Remember left no session")
	}

	if _, err := mem.RememberClaim(ctx, mnemos.ClaimItem{Text: "Beta claim."}); err != nil {
		t.Fatalf("RememberClaim: %v", err)
	}
	afterClaim := mem.LastWriteSession()
	if afterClaim == nil || afterClaim.ID() == "" {
		t.Fatal("RememberClaim left no session")
	}
	if afterClaim.ID() == afterRemember.ID() {
		t.Error("RememberClaim reused the previous session id; expected a fresh session per write")
	}

	if err := mem.RememberEvent(ctx, mnemos.Event{At: time.Now(), Type: "note", Content: "Gamma event."}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}
	afterEvent := mem.LastWriteSession()
	if afterEvent == nil || afterEvent.ID() == "" {
		t.Fatal("RememberEvent left no session")
	}
	if afterEvent.ID() == afterClaim.ID() {
		t.Error("RememberEvent reused the previous session id; expected a fresh session per write")
	}
	if err := afterEvent.VerifyEvidenceChain(); err != nil {
		t.Errorf("final session chain broken: %v", err)
	}
}

// countingTextGen reports a fixed token spend per call and a unique
// claim each time so the on-disk extraction cache never short-circuits
// the model call.
type countingTextGen struct {
	calls  int
	tokens int
}

func (g *countingTextGen) GenerateText(_ context.Context, _ providers.GenerateTextInput) (providers.GenerateTextOutput, error) {
	g.calls++
	return providers.GenerateTextOutput{
		Content:      fmt.Sprintf(`[{"text":"adopted tech %d","type":"fact","confidence":0.9}]`, g.calls),
		Model:        "stub-v1",
		InputTokens:  g.tokens / 2,
		OutputTokens: g.tokens - g.tokens/2,
	}, nil
}

// rememberClaims returns the number of claims currently recallable, used
// to assert whether a write persisted.
func recallCount(t *testing.T, mem mnemos.Memory, text string) int {
	t.Helper()
	res, err := mem.Recall(context.Background(), mnemos.Query{Text: text, Limit: 100})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	return len(res)
}

// TestTokenBudget_PreGateBlocksAlreadyExhausted proves the cumulative
// token budget's PRE-execution gate: once the budget is exhausted, the
// next write is rejected BEFORE the executor persists (no orphaned data),
// while the single write that crosses the threshold still persists and
// reports post-hoc. This pins both halves of the WithTokenBudget
// contract.
func TestTokenBudget_PreGateBlocksAlreadyExhausted(t *testing.T) {
	clearMnemosEnv(t)

	// Per-write spend = 40 tokens. Cumulative budget = 50.
	gen := &countingTextGen{tokens: 40}
	mem, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace=gov_pregate"),
		mnemos.WithSharedProvider(gen, nil),
		mnemos.WithTokenBudget(50),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	ctx := context.Background()

	// Unique nonce defeats the on-disk LLM extraction cache so the model
	// (and thus the token report) actually runs on every write.
	nonce := time.Now().UnixNano()

	// Write 1: cumulative 0 -> 40, under the 50 cap. Persists, no error.
	if err := mem.Remember(ctx, mnemos.Item{Type: "fact", Content: fmt.Sprintf("first adopted tech %d", nonce)}); err != nil {
		t.Fatalf("write 1 should succeed under budget: %v", err)
	}
	afterFirst := recallCount(t, mem, "adopted tech")
	if afterFirst == 0 {
		t.Fatal("write 1 did not persist")
	}

	// Write 2 crosses the threshold (cumulative 40 -> 80 >= 50): it
	// still PERSISTS (its cost wasn't known until the model ran) and
	// REPORTS the budget breach post-hoc.
	err = mem.Remember(ctx, mnemos.Item{Type: "fact", Content: fmt.Sprintf("second adopted tech %d", nonce)})
	if err == nil {
		t.Fatal("threshold-crossing write should report a budget error post-hoc")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "budget") {
		t.Errorf("crossing write error %q does not mention budget", err)
	}
	afterSecond := recallCount(t, mem, "adopted tech")
	if afterSecond <= afterFirst {
		t.Errorf("threshold-crossing write did not persist (count %d -> %d)", afterFirst, afterSecond)
	}

	callsBeforeThird := gen.calls

	// Write 3: budget is ALREADY exhausted (cumulative 80 >= 50). It must
	// be rejected PRE-execution — the model must NOT run and nothing must
	// persist (no orphaned data).
	err = mem.Remember(ctx, mnemos.Item{Type: "fact", Content: fmt.Sprintf("third adopted tech %d", nonce)})
	if err == nil {
		t.Fatal("already-exhausted write should be rejected")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "budget") {
		t.Errorf("pre-gate error %q does not mention budget", err)
	}
	if gen.calls != callsBeforeThird {
		t.Errorf("pre-gate did not run before the executor: model invoked %d extra times", gen.calls-callsBeforeThird)
	}
	afterThird := recallCount(t, mem, "adopted tech")
	if afterThird != afterSecond {
		t.Errorf("already-exhausted write persisted orphaned data (count %d -> %d)", afterSecond, afterThird)
	}
}

// TestRemember_CtxCanceledLeavesSession verifies that cancelling the
// context still leaves a retrievable governance session — the truthful
// behaviour for the hard-error path — and surfaces an error.
func TestRemember_CtxCanceledLeavesSession(t *testing.T) {
	mem := newGovernedMemory(t, "gov_ctx_cancel")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := mem.Remember(ctx, mnemos.Item{Type: "fact", Content: "cancelled write"})
	if err == nil {
		t.Fatal("expected error when context is cancelled before the write completes")
	}
	// The session must still be recoverable so the failed write isn't
	// trail-less.
	if ws := mem.LastWriteSession(); ws == nil || ws.ID() == "" {
		t.Error("cancelled write left no auditable session")
	}
}

// TestConcurrentWrites_EachLeavesVerifiableSession exercises the
// documented concurrency contract: a single Memory is safe for
// concurrent writes, each producing its own verifiable evidence chain,
// and LastWriteSession reflects some completed write.
func TestConcurrentWrites_EachLeavesVerifiableSession(t *testing.T) {
	mem := newGovernedMemory(t, "gov_concurrent")
	ctx := context.Background()

	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n*3)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := mem.Remember(ctx, mnemos.Item{Type: "fact", Content: fmt.Sprintf("concurrent fact %d", i)}); err != nil {
				errs <- err
			}
			if _, err := mem.RememberClaim(ctx, mnemos.ClaimItem{Text: fmt.Sprintf("concurrent claim %d", i)}); err != nil {
				errs <- err
			}
			if err := mem.RememberEvent(ctx, mnemos.Event{At: time.Now(), Type: "note", Content: fmt.Sprintf("concurrent event %d", i)}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent write failed: %v", err)
	}

	ws := mem.LastWriteSession()
	if ws == nil || ws.ID() == "" {
		t.Fatal("no session recorded after concurrent writes")
	}
	if err := ws.VerifyEvidenceChain(); err != nil {
		t.Errorf("last session chain broken: %v", err)
	}
}

// TestTokenBudget_BlocksWrite verifies a token budget below a single
// write's reported spend blocks the write with a budget error. Uses a
// shared TextGenerator that reports token usage and a 1-token budget.
func TestTokenBudget_BlocksWrite(t *testing.T) {
	clearMnemosEnv(t)
	t.Setenv("MNEMOS_AXI_MAX_TOKENS", "1")

	gen := &recordingTextGen{response: `[{"text":"We adopted Go.","type":"fact","confidence":0.9}]`}
	mem, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace=gov_budget"),
		mnemos.WithSharedProvider(gen, nil),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })

	// Unique content keeps the on-disk LLM extraction cache from
	// short-circuiting the model call (and thus the token report) on
	// repeat runs.
	uniq := time.Now().UnixNano()
	err = mem.Remember(context.Background(), mnemos.Item{
		Type:    "fact",
		Content: fmt.Sprintf("Budget test %d: we adopted Go for the backend.", uniq),
		Source:  "test",
	})
	if gen.calls == 0 {
		t.Fatal("TextGenerator never invoked; extractor fell back to rules — budget can't engage")
	}
	if err == nil {
		t.Fatal("expected budget error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "budget") {
		t.Errorf("error %q does not mention budget", err)
	}
}
