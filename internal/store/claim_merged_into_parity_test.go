package store_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// A merged belief is kept as a redirect to the belief that absorbed it (#387).
// Every backend must persist the pointer, return it on read, find it by text
// key, and keep it when a later write supplies none: re-extraction rebuilds a
// claim from text and never carries merged_into, so a blind
// `merged_into = excluded.merged_into` would erase every redirect the next time
// the same text was ingested.
func TestClaims_MergedIntoRoundTripsOnEveryBackend(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	const text = "the billing service retries failed charges three times"

	for _, b := range openBackends(t) {
		loser := domain.Claim{
			ID: "c-merged-loser", Text: text, Type: domain.ClaimTypeFact,
			Confidence: 0.8, Status: domain.ClaimStatusDeprecated, CreatedAt: at,
			MergedInto: "c-merged-winner",
		}
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{loser}); err != nil {
			t.Fatalf("%s: upsert: %v", b.name, err)
		}

		got, err := b.conn.Claims.ListByIDs(ctx, []string{loser.ID})
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: read back: %v (%d rows)", b.name, err, len(got))
		}
		if got[0].MergedInto != loser.MergedInto {
			t.Fatalf("%s: merged_into read back as %q, want %q", b.name, got[0].MergedInto, loser.MergedInto)
		}

		rewritten := loser
		rewritten.MergedInto = ""
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{rewritten}); err != nil {
			t.Fatalf("%s: re-upsert: %v", b.name, err)
		}
		got, err = b.conn.Claims.ListByIDs(ctx, []string{loser.ID})
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: read back after re-upsert: %v", b.name, err)
		}
		if got[0].MergedInto != loser.MergedInto {
			t.Fatalf("%s: a write without merged_into erased the redirect (got %q)", b.name, got[0].MergedInto)
		}

		redirects, err := b.conn.Claims.ListMergedByTextKeys(ctx, []string{domain.ClaimTextKey(text)})
		if err != nil {
			t.Fatalf("%s: list merged by text key: %v", b.name, err)
		}
		if len(redirects) != 1 || redirects[0].ID != loser.ID || redirects[0].MergedInto != loser.MergedInto {
			t.Fatalf("%s: ListMergedByTextKeys = %+v, want the redirect", b.name, redirects)
		}
		none, err := b.conn.Claims.ListMergedByTextKeys(ctx, []string{domain.ClaimTextKey("an unrelated belief")})
		if err != nil || len(none) != 0 {
			t.Fatalf("%s: unrelated key returned %+v (%v)", b.name, none, err)
		}
	}
}

// A recorded-as-of read walks merges backwards (ADR 0036 step 2): from the claim
// that holds a merged belief's evidence now to the redirects that point at it.
// Every backend must return exactly the direct redirects of the ids asked for,
// oldest first, and nothing for a live claim or an unknown id.
func TestClaims_ListMergedIntoOnEveryBackend(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)

	for _, b := range openBackends(t) {
		claims := []domain.Claim{
			{ID: "c-into-winner", Text: "deploys go out on tuesdays", Type: domain.ClaimTypeFact,
				Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: at.Add(2 * time.Hour)},
			{ID: "c-into-b", Text: "deploys happen every tuesday", Type: domain.ClaimTypeFact,
				Confidence: 0.8, Status: domain.ClaimStatusDeprecated, CreatedAt: at.Add(time.Hour), MergedInto: "c-into-winner"},
			{ID: "c-into-a", Text: "we deploy on tuesdays", Type: domain.ClaimTypeFact,
				Confidence: 0.8, Status: domain.ClaimStatusDeprecated, CreatedAt: at, MergedInto: "c-into-winner"},
			{ID: "c-into-chain", Text: "tuesday is deploy day", Type: domain.ClaimTypeFact,
				Confidence: 0.8, Status: domain.ClaimStatusDeprecated, CreatedAt: at, MergedInto: "c-into-a"},
			{ID: "c-into-other", Text: "an unrelated live belief", Type: domain.ClaimTypeFact,
				Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: at},
		}
		if err := b.conn.Claims.Upsert(ctx, claims); err != nil {
			t.Fatalf("%s: upsert: %v", b.name, err)
		}

		got, err := b.conn.Claims.ListMergedInto(ctx, []string{"c-into-winner"})
		if err != nil {
			t.Fatalf("%s: ListMergedInto: %v", b.name, err)
		}
		if len(got) != 2 || got[0].ID != "c-into-a" || got[1].ID != "c-into-b" {
			t.Fatalf("%s: ListMergedInto(winner) = %v, want [c-into-a c-into-b] oldest first", b.name, claimIDs(got))
		}
		if got[0].MergedInto != "c-into-winner" || !got[0].CreatedAt.Equal(at) {
			t.Fatalf("%s: redirect read back as merged_into=%q created_at=%v", b.name, got[0].MergedInto, got[0].CreatedAt)
		}

		chain, err := b.conn.Claims.ListMergedInto(ctx, []string{"c-into-a", "c-into-other", "no-such-claim"})
		if err != nil {
			t.Fatalf("%s: ListMergedInto chain: %v", b.name, err)
		}
		if len(chain) != 1 || chain[0].ID != "c-into-chain" {
			t.Fatalf("%s: ListMergedInto(c-into-a, live, unknown) = %v, want only the direct redirect c-into-chain", b.name, claimIDs(chain))
		}

		none, err := b.conn.Claims.ListMergedInto(ctx, nil)
		if err != nil || len(none) != 0 {
			t.Fatalf("%s: no ids returned %v (%v)", b.name, claimIDs(none), err)
		}
	}
}
