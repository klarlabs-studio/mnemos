package store_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/trust"
)

// Canonical trust (ADR 0026) reads a belief's own time constant, its last
// explicit confirmation and its applied credit. The old port could not carry any of
// them, which is why stored trust ignored all three. Every backend must now
// hand the scorer all three, read from what it stored, and persist exactly
// trust.At of what it handed over.
func TestTrustInput_CarriesPerBeliefInputsAcrossBackends(t *testing.T) {
	backends := openBackends(t)
	ctx := context.Background()
	created := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	confirmedAt := time.Date(2026, 9, 20, 8, 15, 0, 0, time.UTC)
	rehearsedAt := time.Date(2026, 9, 29, 8, 15, 0, 0, time.UTC)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	const id = "c-trust-input"

	for _, b := range backends {
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{
			ID: id, Text: "the build cache is warm after deploy", Type: domain.ClaimTypeFact,
			Confidence: 0.9, Status: domain.ClaimStatusActive, CreatedAt: created,
		}}); err != nil {
			t.Fatalf("%s: upsert: %v", b.name, err)
		}
		if err := b.conn.Claims.MarkConfirmed(ctx, id, confirmedAt); err != nil {
			t.Fatalf("%s: mark confirmed: %v", b.name, err)
		}
		// A later rehearsal (what recall and replay do) sets the time constant
		// here, and must NOT reach the scorer as a confirmation.
		if err := b.conn.Claims.MarkVerified(ctx, id, rehearsedAt, 14); err != nil {
			t.Fatalf("%s: mark verified: %v", b.name, err)
		}
		writer, ok := b.conn.Claims.(ports.BeliefCreditWriter)
		if !ok {
			t.Fatalf("%s: not a BeliefCreditWriter", b.name)
		}
		if err := writer.ApplyBeliefCredit(ctx, id, map[string]float64{
			domain.CreditAppliedComponentKey: -0.12,
			"credit:d1:" + id:                -0.12,
		}, 0); err != nil {
			t.Fatalf("%s: apply credit: %v", b.name, err)
		}

		scorer := b.conn.Claims.(ports.TrustScorer)
		var seen domain.TrustInput
		if _, err := scorer.RecomputeTrust(ctx, domain.TrustScoring{At: now, ModelVersion: trust.ModelVersion, Score: func(in domain.TrustInput) float64 {
			seen = in
			return trust.At(in, now)
		}}); err != nil {
			t.Fatalf("%s: recompute: %v", b.name, err)
		}

		if !seen.LastConfirmed.Equal(confirmedAt) {
			t.Errorf("%s: LastConfirmed reached the scorer as %v, want the confirmation %v (not the rehearsal %v)",
				b.name, seen.LastConfirmed, confirmedAt, rehearsedAt)
		}
		if seen.HalfLifeDays != 14 {
			t.Errorf("%s: HalfLifeDays reached the scorer as %v, want 14", b.name, seen.HalfLifeDays)
		}
		if seen.Credit != -0.12 {
			t.Errorf("%s: applied credit reached the scorer as %v, want -0.12", b.name, seen.Credit)
		}
		got, err := b.conn.Claims.ListByIDs(ctx, []string{id})
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: read back: %v", b.name, err)
		}
		if want := trust.At(seen, now); got[0].TrustScore != want {
			t.Errorf("%s: stored trust %v, want trust.At = %v", b.name, got[0].TrustScore, want)
		}
		// The stored score is a cache with a provenance (ADR 0026 §5).
		if got[0].TrustModelVersion != trust.ModelVersion || !got[0].TrustComputedAt.Equal(now) {
			t.Errorf("%s: trust stamp = (%q, %v), want (%q, %v)",
				b.name, got[0].TrustModelVersion, got[0].TrustComputedAt, trust.ModelVersion, now)
		}
		// Re-ingesting the claim writes no trust, so it must not touch the stamp.
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{
			ID: id, Text: "the build cache is warm after deploy", Type: domain.ClaimTypeFact,
			Confidence: 0.9, Status: domain.ClaimStatusActive, CreatedAt: created,
		}}); err != nil {
			t.Fatalf("%s: re-upsert: %v", b.name, err)
		}
		again, err := b.conn.Claims.ListByIDs(ctx, []string{id})
		if err != nil || len(again) != 1 {
			t.Fatalf("%s: read back after re-upsert: %v", b.name, err)
		}
		if again[0].TrustModelVersion != trust.ModelVersion || !again[0].TrustComputedAt.Equal(now) {
			t.Errorf("%s: re-ingest changed the trust stamp to (%q, %v)", b.name, again[0].TrustModelVersion, again[0].TrustComputedAt)
		}
	}
}
