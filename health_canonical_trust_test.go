package mnemos

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// low_trust judges canonical trust, not the stored cache: a cache can say
// "trusted" for a belief whose confidence is low on its merits. It grades
// AGE-NEUTRAL trust (ADR 0032), so that is the only thing that can put a
// belief under the floor here.
func TestBrainHealth_LowTrustReadsCanonicalTrustNotTheCache(t *testing.T) {
	m := calibMem(t)
	now := time.Now().UTC()
	seedEvidencedClaim(t, m, "weak", now, domain.Claim{Confidence: 0.2, TrustScore: 0.9}) // stale-high cache

	lt := vitalByName(mustHealth(t, m), "low_trust")
	if lt.Value != 1 {
		t.Errorf("low_trust = %+v; a confidence-0.2 belief is untrusted whatever its cache says", lt)
	}
}

// #481: with the freshness floor at 0.30, a confidence-0.9 belief bottoms out at
// trust 0.27 and used to count as low-trust forever once it was ~99 days old.
// That is age, which staleness reports; on its merits the belief is trusted.
func TestBrainHealth_AgeAloneIsNotLowTrust(t *testing.T) {
	m := calibMem(t)
	old := time.Now().UTC().Add(-400 * 24 * time.Hour)
	seedEvidencedClaim(t, m, "aged", old, domain.Claim{Confidence: 0.9})

	h := mustHealth(t, m)
	if lt := vitalByName(h, "low_trust"); lt.Value != 0 {
		t.Errorf("an aged confidence-0.9 belief is stale, not untrusted: low_trust = %+v", lt)
	}
	if st := vitalByName(h, "staleness"); st.Value != 1 {
		t.Errorf("staleness must report the aged belief: %+v", st)
	}
}

func seedEvidencedClaim(t *testing.T, m *memory, id string, at time.Time, c domain.Claim) {
	t.Helper()
	ctx := context.Background()
	if err := m.conn.Events.Append(ctx, domain.Event{
		ID: "ev-" + id, Content: "observed " + id, SchemaVersion: "v1", SourceInputID: "s",
		Timestamp: at, IngestedAt: at, CreatedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	c.ID, c.Text, c.Type, c.Status, c.CreatedAt, c.ValidFrom = id, "Observed "+id, domain.ClaimTypeFact, domain.ClaimStatusActive, at, at
	if err := m.conn.Claims.Upsert(ctx, []domain.Claim{c}); err != nil {
		t.Fatal(err)
	}
	if err := m.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: id, EventID: "ev-" + id}}); err != nil {
		t.Fatal(err)
	}
}

func mustHealth(t *testing.T, m *memory) BrainHealth {
	t.Helper()
	h, err := m.BrainHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return h
}
