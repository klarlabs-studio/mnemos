package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// ListLiveByTextKeys is how the write path asks "is this belief already held?"
// without loading the brain. Every backend must answer it the same way: live
// claims only (valid time open, not deprecated), matched on the normalised
// text, oldest first.
func TestListLiveByTextKeys_AgreesAcrossBackends(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	const text = "The textkey retry budget is 3 attempts"
	key := domain.ClaimTextKey(text)

	for _, b := range openBackends(t) {
		claims := []domain.Claim{
			textKeyClaim("c-tk-first", text, at),
			textKeyClaim("c-tk-later", "the textkey retry budget is 3 attempts.", at.Add(time.Hour)),
			textKeyClaim("c-tk-deprecated", text, at.Add(-time.Hour)),
			textKeyClaim("c-tk-expired", text, at.Add(-2*time.Hour)),
			textKeyClaim("c-tk-other", "The textkey retry budget is 5 attempts", at),
		}
		claims[2].Status = domain.ClaimStatusDeprecated
		if err := b.conn.Claims.Upsert(ctx, claims); err != nil {
			t.Fatalf("%s: upsert: %v", b.name, err)
		}
		if err := b.conn.Claims.SetValidity(ctx, "c-tk-expired", at); err != nil {
			t.Fatalf("%s: set validity: %v", b.name, err)
		}

		got, err := b.conn.Claims.ListLiveByTextKeys(ctx, []string{key})
		if err != nil {
			t.Fatalf("%s: ListLiveByTextKeys: %v", b.name, err)
		}
		if ids := claimIDs(got); !equalIDs(ids, []string{"c-tk-first", "c-tk-later"}) {
			t.Errorf("%s: live claims for key = %v, want [c-tk-first c-tk-later]", b.name, ids)
		}

		if got, err := b.conn.Claims.ListLiveByTextKeys(ctx, nil); err != nil || len(got) != 0 {
			t.Errorf("%s: no keys = %v, %v; want empty, nil", b.name, got, err)
		}
	}
}

// Rewriting a claim's text must move it to the new text's key, or the
// duplicate check would match a claim on words it no longer says.
func TestListLiveByTextKeys_FollowsTextRewrite(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	const before, after = "The rewrite cache is warmed on deploy", "The rewrite cache is warmed on boot"

	for _, b := range openBackends(t) {
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{textKeyClaim("c-tk-rewrite", before, at)}); err != nil {
			t.Fatalf("%s: upsert: %v", b.name, err)
		}
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{textKeyClaim("c-tk-rewrite", after, at)}); err != nil {
			t.Fatalf("%s: rewrite: %v", b.name, err)
		}
		if got, _ := b.conn.Claims.ListLiveByTextKeys(ctx, []string{domain.ClaimTextKey(before)}); len(got) != 0 {
			t.Errorf("%s: old text still finds the rewritten claim: %v", b.name, claimIDs(got))
		}
		if got, _ := b.conn.Claims.ListLiveByTextKeys(ctx, []string{domain.ClaimTextKey(after)}); !equalIDs(claimIDs(got), []string{"c-tk-rewrite"}) {
			t.Errorf("%s: new text does not find the rewritten claim: %v", b.name, claimIDs(got))
		}
	}
}

// A brain written before the key existed has every row at the empty key. The
// next Open must backfill it, or the duplicate check would miss every belief
// the brain already holds — which is where the duplicates are.
func TestListLiveByTextKeys_BackfillsLegacyRowsOnOpen(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	const text = "The legacy brain predates the text key"

	for _, b := range openBackends(t) {
		raw, ok := b.conn.Raw.(interface {
			ExecContext(context.Context, string, ...any) (sql.Result, error)
		})
		if !ok {
			continue // memory derives the key from the text; there is no column to backfill
		}
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{textKeyClaim("c-tk-legacy", text, at)}); err != nil {
			t.Fatalf("%s: upsert: %v", b.name, err)
		}
		if _, err := raw.ExecContext(ctx, `UPDATE claims SET text_key = '' WHERE id = 'c-tk-legacy'`); err != nil {
			t.Fatalf("%s: simulate legacy row: %v", b.name, err)
		}

		reopened, err := store.Open(ctx, b.dsn)
		if err != nil {
			t.Fatalf("%s: reopen: %v", b.name, err)
		}
		got, err := reopened.Claims.ListLiveByTextKeys(ctx, []string{domain.ClaimTextKey(text)})
		_ = reopened.Close()
		if err != nil {
			t.Fatalf("%s: ListLiveByTextKeys after reopen: %v", b.name, err)
		}
		if !equalIDs(claimIDs(got), []string{"c-tk-legacy"}) {
			t.Errorf("%s: legacy row not backfilled on open: %v", b.name, claimIDs(got))
		}
	}
}

func textKeyClaim(id, text string, at time.Time) domain.Claim {
	return domain.Claim{
		ID: id, Text: text, Type: domain.ClaimTypeFact, Confidence: 0.8,
		Status: domain.ClaimStatusActive, CreatedAt: at, CreatedBy: "tester",
	}
}

func claimIDs(claims []domain.Claim) []string {
	ids := make([]string, len(claims))
	for i, c := range claims {
		ids[i] = c.ID
	}
	return ids
}

func equalIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
