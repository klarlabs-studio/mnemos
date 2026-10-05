package store_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// UnlinkEvidence must remove exactly one (claim, event) link on every backend,
// leave the claim's other links alone, and be idempotent.
func TestUnlinkEvidence_RemovesOnlyThatLinkAcrossBackends(t *testing.T) {
	backends := openBackends(t)
	ctx := context.Background()
	at := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	for _, b := range backends {
		for _, id := range []string{"ev-unlink-a", "ev-unlink-b"} {
			if err := b.conn.Events.Append(ctx, domain.Event{
				ID: id, Content: "source " + id, SchemaVersion: "v1", SourceInputID: "src-" + id,
				Timestamp: at, IngestedAt: at, CreatedBy: "tester",
			}); err != nil {
				t.Fatalf("%s: append %s: %v", b.name, id, err)
			}
		}
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{
			ID: "c-unlink", Text: "the cache is warmed on deploy", Type: domain.ClaimTypeFact,
			Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: at,
		}}); err != nil {
			t.Fatalf("%s: upsert: %v", b.name, err)
		}
		if err := b.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
			{ClaimID: "c-unlink", EventID: "ev-unlink-a"},
			{ClaimID: "c-unlink", EventID: "ev-unlink-b"},
		}); err != nil {
			t.Fatalf("%s: link: %v", b.name, err)
		}

		for range 2 { // the second call must be a no-op, not an error
			if err := b.conn.Claims.UnlinkEvidence(ctx, "c-unlink", "ev-unlink-a"); err != nil {
				t.Fatalf("%s: unlink: %v", b.name, err)
			}
		}

		links, err := b.conn.Claims.ListEvidenceByClaimIDs(ctx, []string{"c-unlink"})
		if err != nil {
			t.Fatalf("%s: list evidence: %v", b.name, err)
		}
		if len(links) != 1 || links[0].EventID != "ev-unlink-b" {
			t.Errorf("%s: evidence after unlink = %+v, want only ev-unlink-b", b.name, links)
		}
		if got, err := b.conn.Claims.ListByIDs(ctx, []string{"c-unlink"}); err != nil || len(got) != 1 {
			t.Errorf("%s: claim did not survive unlinking one of its links: %v %v", b.name, got, err)
		}
	}
}
