package recorded_test

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/recorded"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/memory"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// A chain of merges straddling T: c-first is merged into c-second before T,
// c-second into c-third after it. At T the brain held c-second (carrying
// c-first's evidence and its contradiction), not c-first, which was already a
// redirect, and not c-third, which did not exist.
func TestViews_MergeChainStraddlingT(t *testing.T) {
	for name, dsn := range map[string]string{
		"memory": "memory://recorded-merge-chain",
		"sqlite": "sqlite://" + filepath.Join(t.TempDir(), "chain.db"),
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			conn, err := store.Open(ctx, dsn)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			t.Cleanup(func() { _ = conn.Close() })

			add := func(eventID, claimID, text string) {
				t.Helper()
				now := time.Now().UTC()
				if err := conn.Events.Append(ctx, domain.Event{
					ID: eventID, RunID: "chain", SchemaVersion: "v1", SourceInputID: "chain",
					Content: text, Timestamp: now, IngestedAt: now,
				}); err != nil {
					t.Fatalf("append %s: %v", eventID, err)
				}
				if err := conn.Claims.Upsert(ctx, []domain.Claim{{
					ID: claimID, Text: text, Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive,
					Confidence: 0.8, CreatedAt: now,
				}}); err != nil {
					t.Fatalf("upsert %s: %v", claimID, err)
				}
				if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: claimID, EventID: eventID}}); err != nil {
					t.Fatalf("evidence %s: %v", claimID, err)
				}
			}
			merge := func(dup, winner string) {
				t.Helper()
				if _, err := pipeline.ApplySemanticDedupe(ctx, conn, pipeline.SemanticDedupePlan{
					Merges: []pipeline.SemanticMerge{{WinnerID: winner, DuplicateIDs: []string{dup}}},
				}); err != nil {
					t.Fatalf("merge %s into %s: %v", dup, winner, err)
				}
			}
			pause := func() { time.Sleep(5 * time.Millisecond) }

			add("ev-first", "c-first", "deploys go out on tuesdays")
			add("ev-other", "c-other", "deploys never go out on tuesdays")
			if err := conn.Relationships.Upsert(ctx, []domain.Relationship{{
				ID: "rel-other-first", Type: domain.RelationshipTypeContradicts,
				FromClaimID: "c-other", ToClaimID: "c-first", CreatedAt: time.Now().UTC(),
			}}); err != nil {
				t.Fatalf("edge: %v", err)
			}
			pause()
			add("ev-second", "c-second", "deploys happen on tuesdays")
			merge("c-first", "c-second")
			pause()
			at := time.Now().UTC()
			pause()
			add("ev-third", "c-third", "deploys happen every tuesday")
			merge("c-second", "c-third")

			claims := recorded.Claims{ClaimRepository: conn.Claims, Events: conn.Events, At: at}
			rels := recorded.Relationships{RelationshipRepository: conn.Relationships, Claims: conn.Claims, At: at}

			got, err := claims.ListByEventIDs(ctx, []string{"ev-first", "ev-second", "ev-third"})
			if err != nil {
				t.Fatalf("ListByEventIDs: %v", err)
			}
			if ids := idsOf(got); len(ids) != 1 || ids[0] != "c-second" {
				t.Fatalf("claims reached at T = %v, want [c-second]", ids)
			}
			if c := got[0]; c.Status != domain.ClaimStatusActive || c.MergedInto != "" {
				t.Errorf("c-second at T reads status=%q merged_into=%q, want active and unmerged", c.Status, c.MergedInto)
			}

			links, err := claims.ListEvidenceByClaimIDs(ctx, []string{"c-second"})
			if err != nil {
				t.Fatalf("ListEvidenceByClaimIDs: %v", err)
			}
			var events []string
			for _, l := range links {
				if l.ClaimID != "c-second" {
					t.Errorf("evidence for %s returned when only c-second was asked", l.ClaimID)
				}
				events = append(events, l.EventID)
			}
			sort.Strings(events)
			if len(events) != 2 || events[0] != "ev-first" || events[1] != "ev-second" {
				t.Errorf("c-second's evidence at T = %v, want [ev-first ev-second] (ev-third was ingested after T)", events)
			}

			edges, err := rels.ListByClaimIDs(ctx, []string{"c-second", "c-other"})
			if err != nil {
				t.Fatalf("ListByClaimIDs: %v", err)
			}
			if len(edges) != 1 || edges[0].FromClaimID != "c-other" || edges[0].ToClaimID != "c-second" {
				t.Fatalf("edges at T = %+v, want c-other→c-second (c-first's edge, moved by the merge before T)", edges)
			}

			all, err := claims.ListAll(ctx)
			if err != nil {
				t.Fatalf("ListAll: %v", err)
			}
			state := map[string]domain.Claim{}
			for _, c := range all {
				state[c.ID] = c
			}
			if _, ok := state["c-third"]; ok {
				t.Errorf("ListAll at T contains c-third, recorded after T")
			}
			if c := state["c-first"]; c.Status != domain.ClaimStatusDeprecated || c.MergedInto != "c-second" {
				t.Errorf("c-first at T reads status=%q merged_into=%q, want the redirect to c-second it already was", c.Status, c.MergedInto)
			}
			if c := state["c-second"]; c.Status != domain.ClaimStatusActive || c.MergedInto != "" {
				t.Errorf("c-second in ListAll at T reads status=%q merged_into=%q, want active and unmerged", c.Status, c.MergedInto)
			}
		})
	}
}

func idsOf(claims []domain.Claim) []string {
	out := make([]string, 0, len(claims))
	for _, c := range claims {
		out = append(out, c.ID)
	}
	sort.Strings(out)
	return out
}
