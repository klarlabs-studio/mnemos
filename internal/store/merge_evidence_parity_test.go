package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/recorded"
)

// ADR 0036 §8: a merge closes the loser's own evidence links, so a read from
// before the merge attributes each claim exactly its own evidence, on every
// backend. Before §8 the loser was attributed all of its winner's evidence
// and reached through every one of the winner's episodes, and the winner
// counted the loser's moved links from the time the loser recorded them.
func TestMerge_ReadBeforeTheMergeSeesEachClaimsOwnEvidence(t *testing.T) {
	ctx := context.Background()
	for _, b := range openBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			now := time.Now().UTC().Add(-time.Minute)
			for _, id := range []string{"ev-mg-loser", "ev-mg-winner"} {
				if err := b.conn.Events.Append(ctx, domain.Event{
					ID: id, Content: "source " + id, SchemaVersion: "v1", SourceInputID: "src-" + id,
					Timestamp: now, IngestedAt: now, CreatedBy: id,
				}); err != nil {
					t.Fatalf("append %s: %v", id, err)
				}
			}
			for _, id := range []string{"cl-mg-loser", "cl-mg-winner"} {
				if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{
					ID: id, Text: "the deploy pipeline caches images " + id, Type: domain.ClaimTypeFact,
					Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: now,
				}}); err != nil {
					t.Fatalf("upsert %s: %v", id, err)
				}
			}
			if err := b.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
				{ClaimID: "cl-mg-loser", EventID: "ev-mg-loser", CreatedAt: now},
				{ClaimID: "cl-mg-winner", EventID: "ev-mg-winner", CreatedAt: now},
			}); err != nil {
				t.Fatalf("link: %v", err)
			}
			beforeMerge := time.Now().UTC()
			time.Sleep(5 * time.Millisecond)
			if _, err := pipeline.ApplySemanticDedupe(ctx, b.conn, pipeline.SemanticDedupePlan{
				Merges: []pipeline.SemanticMerge{{WinnerID: "cl-mg-winner", DuplicateIDs: []string{"cl-mg-loser"}}},
			}); err != nil {
				t.Fatalf("merge: %v", err)
			}

			view := recorded.Claims{ClaimRepository: b.conn.Claims, Events: b.conn.Events, At: beforeMerge}
			for claim, want := range map[string]string{"cl-mg-loser": "ev-mg-loser", "cl-mg-winner": "ev-mg-winner"} {
				links, err := view.ListEvidenceByClaimIDs(ctx, []string{claim})
				if err != nil {
					t.Fatalf("recorded evidence of %s: %v", claim, err)
				}
				if got := eventIDsOf(links); got != want {
					t.Errorf("evidence of %s before the merge = %s, want only %s", claim, got, want)
				}
			}
			for event, want := range map[string]string{"ev-mg-loser": "cl-mg-loser", "ev-mg-winner": "cl-mg-winner"} {
				reached, err := view.ListByEventIDs(ctx, []string{event})
				if err != nil {
					t.Fatalf("recorded claims of %s: %v", event, err)
				}
				ids := make([]string, len(reached))
				for i, c := range reached {
					ids[i] = c.ID
				}
				if strings.Join(ids, ",") != want {
					t.Errorf("claims reached through %s before the merge = %v, want only %s", event, ids, want)
				}
			}
			// Today the winner holds both episodes and the loser none.
			live, err := b.conn.Claims.ListEvidenceByClaimIDs(ctx, []string{"cl-mg-winner", "cl-mg-loser"})
			if err != nil || eventIDsOf(live) != "ev-mg-loser,ev-mg-winner" {
				t.Errorf("live evidence after the merge = %+v (%v), want both episodes on the winner", live, err)
			}
			for _, l := range live {
				if l.ClaimID != "cl-mg-winner" {
					t.Errorf("live link %+v survived on the loser", l)
				}
			}
		})
	}
}
