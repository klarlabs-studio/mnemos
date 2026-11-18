package store_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/recorded"
)

// ADR 0037 §8: a deliberate status decision leaves a record even when the
// status does not change, and nothing else does. Every backend must record a
// reassertion (from_status == to_status) for UpsertAssertingStatusAs, record
// nothing for an ordinary write carrying the same status (re-ingest), and
// record a real change exactly once whichever variant made it. A
// recorded-as-of read must not change because of a reassertion row.
func TestClaims_StatusAssertionIsRecordedOnEveryBackend(t *testing.T) {
	ctx := context.Background()
	created := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

	for _, b := range openBackends(t) {
		mustDo := func(what string, err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("%s: %s: %v", b.name, what, err)
			}
		}
		c := domain.Claim{ID: "c-assert", Text: "the billing job retries three times", Type: domain.ClaimTypeFact,
			Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: created}
		mustDo("insert", b.conn.Claims.Upsert(ctx, []domain.Claim{c}))

		c.Status = domain.ClaimStatusDeprecated
		mustDo("retract", b.conn.Claims.UpsertWithReasonAs(ctx, []domain.Claim{c}, "retracted", "carol"))
		// Re-ingest of the same belief: an ordinary write carrying the
		// status it already has. Neither variant may add history.
		mustDo("re-ingest", b.conn.Claims.UpsertWithReasonAs(ctx, []domain.Claim{c}, "pipeline", "ingest"))
		mustDo("plain re-upsert", b.conn.Claims.Upsert(ctx, []domain.Claim{c}))
		// A person deprecates it again on their own judgement.
		mustDo("reassert", b.conn.Claims.UpsertAssertingStatusAs(ctx, []domain.Claim{c}, "reviewed: wrong", "erin"))
		// An asserted change is one row, not two.
		c.Status = domain.ClaimStatusActive
		mustDo("assert change", b.conn.Claims.UpsertAssertingStatusAs(ctx, []domain.Claim{c}, "reviewed: right after all", "frank"))

		got, err := b.conn.Claims.ListStatusHistoryByClaimID(ctx, c.ID)
		mustDo("list history", err)
		want := []domain.ClaimStatusTransition{
			{FromStatus: "", ToStatus: domain.ClaimStatusActive, ChangedBy: domain.SystemUser},
			{FromStatus: domain.ClaimStatusActive, ToStatus: domain.ClaimStatusDeprecated, Reason: "retracted", ChangedBy: "carol"},
			{FromStatus: domain.ClaimStatusDeprecated, ToStatus: domain.ClaimStatusDeprecated, Reason: "reviewed: wrong", ChangedBy: "erin"},
			{FromStatus: domain.ClaimStatusDeprecated, ToStatus: domain.ClaimStatusActive, Reason: "reviewed: right after all", ChangedBy: "frank"},
		}
		if len(got) != len(want) {
			t.Fatalf("%s: %d history rows, want %d: %+v", b.name, len(got), len(want), got)
		}
		for i, w := range want {
			g := got[i]
			if g.ClaimID != c.ID || g.FromStatus != w.FromStatus || g.ToStatus != w.ToStatus ||
				g.Reason != w.Reason || g.ChangedBy != w.ChangedBy || g.ChangedAt.IsZero() {
				t.Errorf("%s: row %d = %+v, want %+v", b.name, i, g, w)
			}
			if g.IsReassertion() != (i == 2) {
				t.Errorf("%s: row %d IsReassertion = %v", b.name, i, g.IsReassertion())
			}
		}
		all, err := b.conn.Claims.ListAllStatusHistory(ctx)
		mustDo("list all history", err)
		if len(all) != len(want) {
			t.Errorf("%s: all-claims history has %d rows, want %d", b.name, len(all), len(want))
		}

		// Recorded-as-of (ADR 0036): the reassertion row changes no status
		// at any instant.
		stored, err := b.conn.Claims.ListByIDs(ctx, []string{c.ID})
		mustDo("read claim", err)
		if len(stored) != 1 {
			t.Fatalf("%s: read %d claims, want 1", b.name, len(stored))
		}
		without := append(append([]domain.ClaimStatusTransition{}, got[:2]...), got[3:]...)
		for _, row := range got {
			for _, at := range []time.Time{row.ChangedAt.Add(-time.Nanosecond), row.ChangedAt, row.ChangedAt.Add(time.Nanosecond)} {
				with, okWith := recorded.Rewind(stored[0], got, nil, at)
				bare, okBare := recorded.Rewind(stored[0], without, nil, at)
				if okWith != okBare || with.Status != bare.Status {
					t.Errorf("%s: as of %s the reassertion row moved status: %s (recorded %v) vs %s (recorded %v)",
						b.name, at.Format(time.RFC3339Nano), with.Status, okWith, bare.Status, okBare)
				}
			}
		}
	}
}
