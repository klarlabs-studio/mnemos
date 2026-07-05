package mnemos

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

// TestConsolidate_ReplayTopK verifies prioritized replay: the sleep pass rehearses
// (bumps freshness / verify count of) the K most salient claims and leaves the
// rest — decision > fact > hypothesis by type-prior salience.
func TestConsolidate_ReplayTopK(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()

	upsertTypedClaim(t, m, "dec", "a consequential decision", domain.ClaimTypeDecision)
	upsertTypedClaim(t, m, "fact", "a plain fact", domain.ClaimTypeFact)
	upsertTypedClaim(t, m, "hyp", "a tentative hypothesis", domain.ClaimTypeHypothesis)

	res, err := m.Consolidate(ctx, ConsolidateOptions{ReplayTopK: 1})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.Replayed != 1 {
		t.Fatalf("Replayed = %d, want 1", res.Replayed)
	}

	claims, err := m.conn.Claims.ListByIDs(ctx, []string{"dec", "fact", "hyp"})
	if err != nil {
		t.Fatal(err)
	}
	vc := map[string]int{}
	for _, c := range claims {
		vc[c.ID] = c.VerifyCount
	}
	if vc["dec"] != 1 {
		t.Errorf("highest-salience claim (decision) should be rehearsed; VerifyCount = %d, want 1", vc["dec"])
	}
	if vc["fact"] != 0 || vc["hyp"] != 0 {
		t.Errorf("only the top-1 should be rehearsed; fact=%d hyp=%d, want 0/0", vc["fact"], vc["hyp"])
	}
}

// TestConsolidate_ReplayOffByDefault verifies no rehearsal without ReplayTopK.
func TestConsolidate_ReplayOffByDefault(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	upsertTypedClaim(t, m, "dec", "a consequential decision", domain.ClaimTypeDecision)

	res, err := m.Consolidate(ctx, ConsolidateOptions{})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.Replayed != 0 {
		t.Errorf("Replayed = %d, want 0 (flag off)", res.Replayed)
	}
	claims, _ := m.conn.Claims.ListByIDs(ctx, []string{"dec"})
	if len(claims) == 1 && claims[0].VerifyCount != 0 {
		t.Errorf("no rehearsal expected; VerifyCount = %d", claims[0].VerifyCount)
	}
}
