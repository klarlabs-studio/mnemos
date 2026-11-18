package store_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/recorded"
	"go.klarlabs.de/mnemos/internal/trust"
)

// ADR 0038 §3: a dependent evidence episode adds no distinct source. Two
// beliefs, each with two evidence episodes by two different authors: one
// second episode is independent, the other cites the first. Every backend
// hands the scorer the same inputs, and the recorded-as-of reconstruction
// agrees with them now and with the pre-dependency count before the citation
// was recorded.
func TestTrustInput_DependentEvidenceIsNotADistinctSourceAcrossBackends(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, b := range openBackends(t) {
		for _, e := range []domain.Event{
			{ID: "td-orig", Content: "original", SourceInputID: "src-orig", CreatedBy: "alice"},
			{ID: "td-indep", Content: "independent", SourceInputID: "src-indep", CreatedBy: "bob"},
			{ID: "td-copy", Content: "copy", SourceInputID: "src-copy", CreatedBy: "carol"},
		} {
			e.SchemaVersion, e.Timestamp, e.IngestedAt = "v1", at.Add(-48*time.Hour), at.Add(-48*time.Hour)
			if err := b.conn.Events.Append(ctx, e); err != nil {
				t.Fatalf("%s: append %s: %v", b.name, e.ID, err)
			}
		}
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{
			{ID: "td-corroborated", Text: "corroborated", Type: domain.ClaimTypeFact, Confidence: 0.7, Status: domain.ClaimStatusActive, CreatedAt: at.Add(-48 * time.Hour)},
			{ID: "td-copied", Text: "copied", Type: domain.ClaimTypeFact, Confidence: 0.7, Status: domain.ClaimStatusActive, CreatedAt: at.Add(-48 * time.Hour)},
		}); err != nil {
			t.Fatalf("%s: upsert: %v", b.name, err)
		}
		if err := b.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
			{ClaimID: "td-corroborated", EventID: "td-orig", CreatedAt: at.Add(-48 * time.Hour)},
			{ClaimID: "td-corroborated", EventID: "td-indep", CreatedAt: at.Add(-48 * time.Hour)},
			{ClaimID: "td-copied", EventID: "td-orig", CreatedAt: at.Add(-48 * time.Hour)},
			{ClaimID: "td-copied", EventID: "td-copy", CreatedAt: at.Add(-48 * time.Hour)},
		}); err != nil {
			t.Fatalf("%s: evidence: %v", b.name, err)
		}
		recordedAt := at.Add(-24 * time.Hour)
		if err := b.conn.Dependencies.Append(ctx, []domain.EventDependency{{
			EventID: "td-copy", DependsOn: "td-orig", Kind: domain.DependencyCopies, Signal: domain.SignalMetadata,
			Reference: "copied_from: td-orig", CreatedAt: recordedAt, CreatedBy: "tester",
		}}); err != nil {
			t.Fatalf("%s: dependency: %v", b.name, err)
		}

		lister, ok := b.conn.Claims.(ports.TrustInputLister)
		if !ok {
			t.Fatalf("%s: not a TrustInputLister", b.name)
		}
		inputs, err := lister.ListTrustInputs(ctx, []string{"td-corroborated", "td-copied"})
		if err != nil {
			t.Fatalf("%s: trust inputs: %v", b.name, err)
		}
		if got, want := inputs["td-corroborated"].EvidenceCount, domain.EffectiveEvidenceCount(2, 2); got != want {
			t.Errorf("%s: corroborated evidence count = %d, want %d (two independent authors)", b.name, got, want)
		}
		if got, want := inputs["td-copied"].EvidenceCount, domain.EffectiveEvidenceCount(1, 2); got != want {
			t.Errorf("%s: copied evidence count = %d, want %d (the copy weighs as a repeat)", b.name, got, want)
		}

		scorer, ok := b.conn.Claims.(ports.ScopedTrustScorer)
		if !ok {
			t.Fatalf("%s: not a ScopedTrustScorer", b.name)
		}
		if _, err := scorer.RecomputeTrustForClaims(ctx, []string{"td-corroborated", "td-copied"}, trust.Scorer(at)); err != nil {
			t.Fatalf("%s: rescore: %v", b.name, err)
		}
		stored, err := b.conn.Claims.ListByIDs(ctx, []string{"td-copied"})
		if err != nil || len(stored) != 1 {
			t.Fatalf("%s: read back: %v", b.name, err)
		}
		if want := trust.At(inputs["td-copied"], at); stored[0].TrustScore != want {
			t.Errorf("%s: stored trust of td-copied = %v, want trust.At of its inputs %v", b.name, stored[0].TrustScore, want)
		}

		// Recorded-as-of (ADR 0036 §7): the reconstruction counts only the
		// dependencies recorded at or before the instant.
		for _, tc := range []struct {
			at   time.Time
			want int
		}{
			{at, domain.EffectiveEvidenceCount(1, 2)},
			{recordedAt.Add(-time.Hour), domain.EffectiveEvidenceCount(2, 2)},
		} {
			view := recorded.Claims{ClaimRepository: b.conn.Claims, Events: b.conn.Events, Dependencies: b.conn.Dependencies, At: tc.at}
			got, err := view.ListByIDs(ctx, []string{"td-copied"})
			if err != nil || len(got) != 1 {
				t.Fatalf("%s: recorded read at %s: %v", b.name, tc.at, err)
			}
			links, err := view.ListEvidenceByClaimIDs(ctx, []string{"td-copied"})
			if err != nil {
				t.Fatalf("%s: recorded evidence: %v", b.name, err)
			}
			if len(links) != 2 {
				t.Fatalf("%s: recorded evidence at %s = %d links, want 2", b.name, tc.at, len(links))
			}
			wantInput := inputs["td-copied"]
			wantInput.EvidenceCount = tc.want
			if want := trust.At(wantInput, tc.at); !closeTo(got[0].TrustScore, want) {
				t.Errorf("%s: recorded trust at %s = %v, want %v (evidence count %d)", b.name, tc.at, got[0].TrustScore, want, tc.want)
			}
		}
	}
}

func closeTo(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}
