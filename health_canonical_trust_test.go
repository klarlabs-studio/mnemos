package mnemos

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// low_trust judges canonical trust NOW, not the stored cache: a cache computed
// weeks ago for a volatile belief still says "trusted", while trust.At on the
// same inputs today is below the floor. low_trust and trust_decay now read the
// same function, so the belief is counted where it belongs instead of by
// neither.
func TestBrainHealth_LowTrustReadsCanonicalTrustNow(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-60 * 24 * time.Hour)
	if err := m.conn.Events.Append(ctx, domain.Event{
		ID: "ev", Content: "deploy pipeline uses runner v2", SchemaVersion: "v1", SourceInputID: "s",
		Timestamp: old, IngestedAt: old, CreatedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	// A stale-high cache: the memory backend keeps a caller-supplied score.
	if err := m.conn.Claims.Upsert(ctx, []domain.Claim{{
		ID: "vol", Text: "Deploy pipeline uses runner v2", Type: domain.ClaimTypeFact,
		Confidence: 0.9, TrustScore: 0.9, Status: domain.ClaimStatusActive, CreatedAt: old, ValidFrom: old,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := m.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: "vol", EventID: "ev"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.conn.Claims.MarkVerified(ctx, "vol", old, 14); err != nil { // 14-day time constant
		t.Fatal(err)
	}

	h, err := m.BrainHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lt := vitalByName(h, "low_trust")
	if lt.Value != 1 {
		t.Errorf("low_trust = %+v; the volatile belief is below the floor now (canonical trust) and must count, whatever its stale cache says", lt)
	}
}
