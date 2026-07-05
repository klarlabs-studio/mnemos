package mnemos

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"

	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// TestExpectations_ReconcileConfirmedAndRefuted verifies the full prediction loop:
// attach a structured expectation, record the observed value, reconcile into a
// surprise + a validates/refutes verdict edge (which the surprise routing then
// consumes), and confirm reconciliation is idempotent.
func TestExpectations_ReconcileConfirmedAndRefuted(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	now := time.Now().UTC()

	upsertTypedClaim(t, m, "cok", "error rate stays low after the deploy", domain.ClaimTypeHypothesis)
	upsertTypedClaim(t, m, "cbad", "latency stays under 100ms after the deploy", domain.ClaimTypeHypothesis)

	if err := m.Expect(ctx, "cok", 0.01, 0.005, now.Add(time.Hour)); err != nil {
		t.Fatalf("Expect cok: %v", err)
	}
	if err := m.Expect(ctx, "cbad", 100, 10, now.Add(time.Hour)); err != nil {
		t.Fatalf("Expect cbad: %v", err)
	}
	if err := m.RecordObservation(ctx, "cok", 0.012); err != nil { // |0.01-0.012| = 0.002 ≤ 0.005 → confirmed
		t.Fatalf("RecordObservation cok: %v", err)
	}
	if err := m.RecordObservation(ctx, "cbad", 250); err != nil { // |100-250| = 150 > 10 → refuted
		t.Fatalf("RecordObservation cbad: %v", err)
	}

	recs, err := m.ReconcileExpectations(ctx)
	if err != nil {
		t.Fatalf("ReconcileExpectations: %v", err)
	}
	byID := map[string]Reconciliation{}
	for _, r := range recs {
		byID[r.ClaimID] = r
	}
	if !byID["cok"].Confirmed {
		t.Errorf("cok within tolerance should be confirmed; got %+v", byID["cok"])
	}
	if byID["cbad"].Confirmed {
		t.Errorf("cbad beyond tolerance should be refuted; got %+v", byID["cbad"])
	}
	if byID["cbad"].Surprise <= 1 {
		t.Errorf("a violated prediction's surprise should exceed 1; got %.3f", byID["cbad"].Surprise)
	}

	// The verdict edges were emitted, so the surprise routing can act on them.
	verdicts, err := m.latestClaimVerdicts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if refuted, ok := verdicts["cok"]; !ok || refuted {
		t.Errorf("cok should carry a validates verdict; got ok=%v refuted=%v", ok, refuted)
	}
	if refuted, ok := verdicts["cbad"]; !ok || !refuted {
		t.Errorf("cbad should carry a refutes verdict; got ok=%v refuted=%v", ok, refuted)
	}

	// Idempotent: resolved expectations don't reconcile again.
	recs2, err := m.ReconcileExpectations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs2) != 0 {
		t.Errorf("resolved expectations should not re-reconcile; got %d", len(recs2))
	}
}

// TestExpectations_SQLiteBackend exercises the sqlite repository through the public
// API (RecordObservation before any Expect errors; a full round-trip reconciles).
func TestExpectations_SQLiteBackend(t *testing.T) {
	for _, k := range []string{"MNEMOS_STORAGE", "MNEMOS_MODE", "MNEMOS_LLM_PROVIDER", "MNEMOS_API_KEY"} {
		t.Setenv(k, "")
	}
	mem, err := New(WithSQLite(filepath.Join(t.TempDir(), "exp.db")), WithPassiveMode())
	if err != nil {
		t.Fatalf("New sqlite: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	ctx := context.Background()

	if err := mem.RecordObservation(ctx, "missing", 1); err == nil {
		t.Error("RecordObservation without an expectation should error")
	}
	if err := mem.Expect(ctx, "c1", 5, 1, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Expect: %v", err)
	}
	if err := mem.RecordObservation(ctx, "c1", 5.5); err != nil { // within tolerance 1
		t.Fatalf("RecordObservation: %v", err)
	}
	recs, err := mem.ReconcileExpectations(ctx)
	if err != nil {
		t.Fatalf("ReconcileExpectations: %v", err)
	}
	if len(recs) != 1 || !recs[0].Confirmed {
		t.Fatalf("sqlite round-trip: want one confirmed reconciliation; got %+v", recs)
	}
}
