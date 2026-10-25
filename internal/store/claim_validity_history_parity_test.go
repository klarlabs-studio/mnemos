package store_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ADR 0034 step 2: valid time and the archive axis are versioned. Every
// backend must append one ValidityChange per real change, with its who, why,
// old and new value, append nothing for a write that changes nothing, and drop
// the history with the claim on a hard delete.
func TestClaims_ValidityChangesAreVersionedOnEveryBackend(t *testing.T) {
	ctx := context.Background()
	created := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	ended := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	archived := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)

	for _, b := range openBackends(t) {
		c := domain.Claim{ID: "c-vh", Text: "the search index rebuilds nightly", Type: domain.ClaimTypeFact,
			Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: created}
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{c}); err != nil {
			t.Fatalf("%s: upsert: %v", b.name, err)
		}
		mustDo := func(what string, err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("%s: %s: %v", b.name, what, err)
			}
		}
		mustDo("supersede", b.conn.Claims.SetValidity(ctx, c.ID, ended, domain.ChangeNote{Reason: "resolve --supersedes c-new", ChangedBy: "alice"}))
		mustDo("supersede again", b.conn.Claims.SetValidity(ctx, c.ID, ended, domain.ChangeNote{Reason: "no-op", ChangedBy: "alice"}))
		mustDo("reopen", b.conn.Claims.SetValidity(ctx, c.ID, time.Time{}, domain.ChangeNote{Reason: "resolution reverted", ChangedBy: "bob"}))
		mustDo("archive", b.conn.Claims.Archive(ctx, c.ID, archived, domain.ChangeNote{Reason: "consolidate: trust below 0.30"}))
		_, err := b.conn.Claims.Unarchive(ctx, []string{c.ID}, domain.ChangeNote{Reason: "reinforced"})
		mustDo("unarchive", err)
		_, err = b.conn.Claims.Unarchive(ctx, []string{c.ID}, domain.ChangeNote{Reason: "no-op"})
		mustDo("unarchive again", err)

		got, err := b.conn.Claims.ListValidityHistory(ctx, c.ID)
		mustDo("list history", err)
		want := []domain.ValidityChange{
			{ClaimID: c.ID, Field: domain.ValidityFieldValidTo, To: ended, Reason: "resolve --supersedes c-new", ChangedBy: "alice"},
			{ClaimID: c.ID, Field: domain.ValidityFieldValidTo, From: ended, Reason: "resolution reverted", ChangedBy: "bob"},
			{ClaimID: c.ID, Field: domain.ValidityFieldArchivedAt, To: archived, Reason: "consolidate: trust below 0.30", ChangedBy: domain.SystemUser},
			{ClaimID: c.ID, Field: domain.ValidityFieldArchivedAt, From: archived, Reason: "reinforced", ChangedBy: domain.SystemUser},
		}
		if len(got) != len(want) {
			t.Fatalf("%s: %d history rows, want %d: %+v", b.name, len(got), len(want), got)
		}
		for i, w := range want {
			g := got[i]
			if g.ClaimID != w.ClaimID || g.Field != w.Field || !g.From.Equal(w.From) || !g.To.Equal(w.To) ||
				g.Reason != w.Reason || g.ChangedBy != w.ChangedBy || g.ChangedAt.IsZero() {
				t.Errorf("%s: row %d = %+v, want %+v (with a ChangedAt)", b.name, i, g, w)
			}
		}
		all, err := b.conn.Claims.ListValidityHistory(ctx, "")
		mustDo("list all history", err)
		if len(all) != len(want) {
			t.Errorf("%s: all-claims history has %d rows, want %d", b.name, len(all), len(want))
		}

		mustDo("delete", b.conn.Claims.DeleteCascade(ctx, c.ID))
		if left, err := b.conn.Claims.ListValidityHistory(ctx, c.ID); err != nil || len(left) != 0 {
			t.Errorf("%s: history survived the claim's hard delete: %+v (%v)", b.name, left, err)
		}
	}
}
