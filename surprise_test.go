package mnemos

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// TestConsolidate_ForgetsRefuted verifies the prediction-error loop closing: a
// claim an observed outcome REFUTED is invalidated by the sleep pass, while a
// validated claim stays and a PROMOTED (human-endorsed) refuted claim is exempt
// (it surfaces as a hypercorrection instead of being silently forgotten).
func TestConsolidate_ForgetsRefuted(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// c1: refuted by an outcome → should be forgotten.
	seedClaim(t, m, "c1", 0.9)
	adjudicate(t, m, "c1", "c1", false, now)
	// c2: validated → should stay.
	seedClaim(t, m, "c2", 0.9)
	adjudicate(t, m, "c2", "c2", true, now)
	// c3: refuted BUT promoted → exempt (hypercorrection, not silent forget).
	if err := m.conn.Claims.Upsert(ctx, []domain.Claim{{
		ID: "c3", Text: "promoted claim", Type: domain.ClaimTypeFact, Confidence: 0.9,
		Status: domain.ClaimStatusActive, Lifecycle: domain.ClaimLifecyclePromoted,
		CreatedAt: now, ValidFrom: now,
	}}); err != nil {
		t.Fatal(err)
	}
	adjudicate(t, m, "c3", "c3", false, now)

	res, err := m.Consolidate(ctx, ConsolidateOptions{ForgetRefuted: true})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.Refuted != 1 {
		t.Fatalf("Refuted = %d, want 1 (only c1; c2 validated, c3 promoted-exempt)", res.Refuted)
	}

	// Verify valid-time: c1 closed, c2 + c3 still open.
	claims, err := m.conn.Claims.ListByIDs(ctx, []string{"c1", "c2", "c3"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{} // id -> invalidated?
	for _, c := range claims {
		got[c.ID] = !c.ValidTo.IsZero()
	}
	if !got["c1"] {
		t.Error("c1 (refuted) must be invalidated")
	}
	if got["c2"] {
		t.Error("c2 (validated) must remain valid")
	}
	if got["c3"] {
		t.Error("c3 (promoted, refuted) must be exempt from forgetting")
	}
}

// TestConsolidate_ForgetRefutedRespectsLatest verifies the latest outcome wins:
// a claim refuted then later re-validated is NOT forgotten.
func TestConsolidate_ForgetRefutedRespectsLatest(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	base := time.Now().UTC()
	seedClaim(t, m, "c1", 0.8)
	adjudicate(t, m, "e_early", "c1", false, base)                // refuted first
	adjudicate(t, m, "e_late", "c1", true, base.Add(2*time.Hour)) // then re-validated

	res, err := m.Consolidate(ctx, ConsolidateOptions{ForgetRefuted: true})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.Refuted != 0 {
		t.Fatalf("Refuted = %d, want 0 (latest outcome re-validated it)", res.Refuted)
	}
}
