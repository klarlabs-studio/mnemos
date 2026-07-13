package mnemos

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

// TestConsolidate_JournalRecordsPassAndBeliefTrust is the ADR-0018 acceptance test: a
// journaled pass writes one consolidation entry (with a PredictiveError snapshot) and one
// belief_trust entry per credit-moved belief; with journaling off, nothing is written.
func TestConsolidate_JournalRecordsPassAndBeliefTrust(t *testing.T) {
	ctx := context.Background()
	m := calibMem(t)
	seedClaim(t, m, "b", 0.8)
	seedDecision(t, m, "d", "b")
	seedObservedExpectation(t, m, "b", 10, 100, 5) // refuted → credit moves trust down

	res, err := m.Consolidate(ctx, ConsolidateOptions{AssignCredit: true, Journal: true})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.Credited != 1 {
		t.Fatalf("Credited = %d, want 1", res.Credited)
	}

	// One pass-level entry carrying a predictive_error snapshot.
	cons, err := m.conn.Journal.List(ctx, domain.JournalKindConsolidation, 10)
	if err != nil {
		t.Fatalf("List consolidation: %v", err)
	}
	if len(cons) != 1 {
		t.Fatalf("consolidation entries = %d, want 1", len(cons))
	}
	if !strings.Contains(cons[0].Data, "predictive_error") || !strings.Contains(cons[0].Data, "result") {
		t.Errorf("pass entry missing result/predictive_error: %s", cons[0].Data)
	}

	// One belief_trust entry for b, with a negative delta (the belief was blamed).
	bt, err := m.conn.Journal.ListBySubject(ctx, "b", 10)
	if err != nil {
		t.Fatalf("ListBySubject: %v", err)
	}
	if len(bt) != 1 {
		t.Fatalf("belief_trust entries for b = %d, want 1", len(bt))
	}
	var payload struct {
		Before float64 `json:"before"`
		After  float64 `json:"after"`
		Delta  float64 `json:"delta"`
	}
	if err := json.Unmarshal([]byte(bt[0].Data), &payload); err != nil {
		t.Fatalf("unmarshal belief_trust: %v", err)
	}
	if payload.Delta >= 0 {
		t.Errorf("a refuted belief's trust delta should be negative, got %v", payload.Delta)
	}
	if payload.After != payload.Before+payload.Delta {
		t.Errorf("delta inconsistent: before=%v after=%v delta=%v", payload.Before, payload.After, payload.Delta)
	}

	// Journaling off: a pass writes nothing.
	m2 := calibMem(t)
	seedClaim(t, m2, "b", 0.8)
	seedDecision(t, m2, "d", "b")
	seedObservedExpectation(t, m2, "b", 10, 100, 5)
	if _, err := m2.Consolidate(ctx, ConsolidateOptions{AssignCredit: true}); err != nil {
		t.Fatal(err)
	}
	if off, _ := m2.conn.Journal.List(ctx, "", 10); len(off) != 0 {
		t.Errorf("journaling off must write nothing, got %d entries", len(off))
	}
}
