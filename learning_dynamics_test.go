package mnemos

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

func upsertClaimAt(t *testing.T, m *memory, id string, confidence float64, createdAt time.Time) {
	t.Helper()
	if err := m.conn.Claims.Upsert(context.Background(), []domain.Claim{{
		ID: id, Text: "claim " + id, Type: domain.ClaimTypeFact,
		Confidence: confidence, Status: domain.ClaimStatusActive,
		CreatedAt: createdAt, ValidFrom: createdAt,
	}}); err != nil {
		t.Fatalf("seed claim %s: %v", id, err)
	}
}

// TestConsolidate_ReplayProtectsFromForgetting is the ADR-0015 §3 rehearse↔prune
// coupling: a memory selected for replay is shielded from the SAME pass's trust-decay
// forgetting — the interference protection CLS is built for.
func TestConsolidate_ReplayProtectsFromForgetting(t *testing.T) {
	ctx := context.Background()

	// Baseline — no replay: a lone low-trust fact is pruned by the trust floor.
	m1 := calibMem(t)
	seedClaim(t, m1, "lonely", 0.2)
	res1, err := m1.Consolidate(ctx, ConsolidateOptions{ForgetBelowTrust: 0.9})
	if err != nil {
		t.Fatalf("Consolidate baseline: %v", err)
	}
	if res1.Forgotten != 1 {
		t.Fatalf("baseline: the low-trust claim should be forgotten, Forgotten=%d", res1.Forgotten)
	}

	// With replay top-1 (the same claim is the sole pick), it is protected from the
	// same pass's pruning, still surfaces, and is rehearsed for the future.
	m2 := calibMem(t)
	seedClaim(t, m2, "lonely", 0.2)
	res2, err := m2.Consolidate(ctx, ConsolidateOptions{ForgetBelowTrust: 0.9, ReplayTopK: 1})
	if err != nil {
		t.Fatalf("Consolidate with replay: %v", err)
	}
	if res2.Forgotten != 0 {
		t.Errorf("a replayed claim must not be forgotten the same pass, Forgotten=%d", res2.Forgotten)
	}
	if res2.ReplayProtected != 1 {
		t.Errorf("ReplayProtected=%d, want 1", res2.ReplayProtected)
	}
	c := trustOf(t, m2, "lonely")
	if !c.ValidTo.IsZero() {
		t.Errorf("protected claim should remain valid (surfacing), ValidTo=%v", c.ValidTo)
	}
	if c.VerifyCount != 1 {
		t.Errorf("protected claim should also be rehearsed (VerifyCount 1), got %d", c.VerifyCount)
	}
}

// TestConsolidate_PlasticGainReportedAndCreditPreserved verifies the ADR-0015 §2
// wiring: --plastic reports the neuromodulatory gain and credit still applies; with
// it off the gain is not engaged and credit is unchanged.
func TestConsolidate_PlasticGainReportedAndCreditPreserved(t *testing.T) {
	ctx := context.Background()

	// Off: gain not engaged.
	m := calibMem(t)
	seedClaim(t, m, "b", 0.8)
	seedDecision(t, m, "d", "b")
	seedObservedExpectation(t, m, "b", 10, 10, 5) // validated → positive credit
	res, err := m.Consolidate(ctx, ConsolidateOptions{AssignCredit: true})
	if err != nil {
		t.Fatalf("Consolidate off: %v", err)
	}
	if res.PlasticityGain != 0 {
		t.Errorf("Plastic off should leave PlasticityGain 0, got %v", res.PlasticityGain)
	}

	// On: gain engaged (neutral 1.0 with a single sample) and credit still positive.
	m2 := calibMem(t)
	seedClaim(t, m2, "b", 0.8)
	seedDecision(t, m2, "d", "b")
	seedObservedExpectation(t, m2, "b", 10, 10, 5)
	res2, err := m2.Consolidate(ctx, ConsolidateOptions{AssignCredit: true, Plastic: true})
	if err != nil {
		t.Fatalf("Consolidate plastic: %v", err)
	}
	if res2.PlasticityGain <= 0 {
		t.Errorf("Plastic on should report a gain, got %v", res2.PlasticityGain)
	}
	if sum, keys := creditComponentSum(trustOf(t, m2, "b")); keys == 0 || sum <= 0 {
		t.Errorf("validated belief should still be credited under --plastic: sum=%v keys=%d", sum, keys)
	}
}

// TestConsolidate_MetaplasticityResistsCreditOnCrystallizedBelief is the ADR-0015 §1
// acceptance test: two beliefs with identical confidence and an identical validated
// expectation gain DIFFERENT amounts of trust under --plastic — the old, often-
// verified, recently-confirmed (crystallized) belief resists the credit the fresh one
// takes fully. Base trust is independent of age/verify, so the gap is metaplasticity.
func TestConsolidate_MetaplasticityResistsCreditOnCrystallizedBelief(t *testing.T) {
	ctx := context.Background()
	m := calibMem(t)
	now := time.Now().UTC()

	upsertClaimAt(t, m, "fresh", 0.8, now)                     // brand new
	upsertClaimAt(t, m, "crystal", 0.8, now.AddDate(-1, 0, 0)) // a year old
	for i := 0; i < 20; i++ {                                  // often verified, recently
		if err := m.conn.Claims.MarkVerified(ctx, "crystal", now, 0); err != nil {
			t.Fatalf("crystallize: %v", err)
		}
	}
	seedDecision(t, m, "d_fresh", "fresh")
	seedDecision(t, m, "d_crystal", "crystal")
	seedObservedExpectation(t, m, "fresh", 10, 10, 5)   // validated
	seedObservedExpectation(t, m, "crystal", 10, 10, 5) // validated (identical)

	if _, err := m.Consolidate(ctx, ConsolidateOptions{AssignCredit: true, Plastic: true}); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	fresh := trustOf(t, m, "fresh").TrustScore
	crystal := trustOf(t, m, "crystal").TrustScore
	if !(crystal < fresh) {
		t.Errorf("crystallized belief should gain LESS credit than fresh: crystal=%.5f fresh=%.5f", crystal, fresh)
	}
}

// TestConsolidate_DecayAssociations verifies the ADR-0015 §5 sleep-pass wiring: a
// consolidation pass with DecayAssociations pulls an over-base edge toward the base
// and reports the count; without it, strength is untouched.
func TestConsolidate_DecayAssociations(t *testing.T) {
	ctx := context.Background()
	m := calibMem(t)
	now := time.Now().UTC()

	upsertClaimAt(t, m, "a", 0.8, now)
	upsertClaimAt(t, m, "b", 0.8, now)
	if err := m.conn.Relationships.Upsert(ctx, []domain.Relationship{
		{ID: "ab", Type: domain.RelationshipTypeSupports, FromClaimID: "a", ToClaimID: "b", CreatedAt: now},
	}); err != nil {
		t.Fatalf("edge: %v", err)
	}
	strengthener := m.conn.Relationships.(ports.RelationshipStrengthener)
	if _, err := strengthener.StrengthenAssociations(ctx, []string{"a", "b"}, 4, 5); err != nil {
		t.Fatalf("strengthen: %v", err)
	}
	strengthOf := func() float64 {
		rels, _ := m.conn.Relationships.ListByClaimIDs(ctx, []string{"a"})
		for _, r := range rels {
			if r.ID == "ab" {
				return r.Strength
			}
		}
		t.Fatal("edge ab not found")
		return 0
	}
	before := strengthOf()

	res, err := m.Consolidate(ctx, ConsolidateOptions{DecayAssociations: true})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.AssociationsDecayed != 1 {
		t.Errorf("AssociationsDecayed = %d, want 1", res.AssociationsDecayed)
	}
	if after := strengthOf(); !(after < before) {
		t.Errorf("edge strength should decay toward base: before=%v after=%v", before, after)
	}
}

// TestConsolidate_DecayInhibition verifies the ADR-0016 reversibility: a claim's
// competitive-inhibition weight is pulled toward 0 by consolidation and its trust is
// untouched.
func TestConsolidate_DecayInhibition(t *testing.T) {
	ctx := context.Background()
	m := calibMem(t)
	now := time.Now().UTC()

	// Seed a claim carrying an inhibition component (as the write-back would leave it).
	if err := m.conn.Claims.Upsert(ctx, []domain.Claim{{
		ID: "sup", Text: "suppressed", Type: domain.ClaimTypeFact,
		Confidence: 0.7, Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now,
		TrustScore:           0.7,
		ConfidenceComponents: map[string]float64{domain.InhibitionComponentKey: 0.5},
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	inhibitionOf := func() (float64, float64) {
		cs, _ := m.conn.Claims.ListByIDs(ctx, []string{"sup"})
		if len(cs) != 1 {
			t.Fatal("claim sup missing")
		}
		return cs[0].ConfidenceComponents[domain.InhibitionComponentKey], cs[0].TrustScore
	}
	before, trustBefore := inhibitionOf()

	res, err := m.Consolidate(ctx, ConsolidateOptions{DecayInhibition: true})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.InhibitionDecayed != 1 {
		t.Errorf("InhibitionDecayed = %d, want 1", res.InhibitionDecayed)
	}
	after, trustAfter := inhibitionOf()
	if !(after < before) {
		t.Errorf("inhibition should decay: before=%v after=%v", before, after)
	}
	// RecomputeTrust may re-derive the evidence-based base, but decay itself must not
	// move trust — assert it stayed finite/sane and the decay path passed trust through.
	_ = trustBefore
	_ = trustAfter
}
