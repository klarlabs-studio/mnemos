package store_test

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/recorded"
)

// ADR 0036 §8: an evidence link or an association that is ended is closed, not
// deleted, on every backend. Asserted on what each store RETURNS:
//
//   - every live read (evidence by claim, all evidence, claims by event,
//     edges by claim, all edges, edge count) stops seeing the closed row;
//   - the closed row comes back from the closed listers with the time it was
//     recorded, the time it was closed, who closed it and why;
//   - a merge (RepointEndpoint) closes exactly the edges it drops, with the
//     endpoints they had, and a recorded-as-of read before the merge sees them;
//   - destroying a claim or an event clears its closed rows.
func TestClosedLinksAndEdges_AgreeAcrossBackends(t *testing.T) {
	ctx := context.Background()
	monday := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
	tuesday := monday.Add(24 * time.Hour)
	thursday := monday.Add(72 * time.Hour)

	for _, b := range openBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			for _, id := range []string{"ev-cl-a", "ev-cl-b"} {
				if err := b.conn.Events.Append(ctx, domain.Event{
					ID: id, Content: "source " + id, SchemaVersion: "v1", SourceInputID: "src-" + id,
					Timestamp: monday, IngestedAt: monday, CreatedBy: "tester",
				}); err != nil {
					t.Fatalf("append %s: %v", id, err)
				}
			}
			for _, id := range []string{"cl-cl-loser", "cl-cl-winner", "cl-cl-other"} {
				if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{
					ID: id, Text: "the cache is flushed on deploy " + id, Type: domain.ClaimTypeFact,
					Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: monday,
				}}); err != nil {
					t.Fatalf("upsert %s: %v", id, err)
				}
			}
			if err := b.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
				{ClaimID: "cl-cl-winner", EventID: "ev-cl-a", CreatedAt: monday},
				{ClaimID: "cl-cl-winner", EventID: "ev-cl-b", CreatedAt: monday},
			}); err != nil {
				t.Fatalf("link: %v", err)
			}

			// Evidence: close one link.
			note := domain.ChangeNote{Reason: "retracted: test", ChangedBy: "tester"}
			if err := b.conn.Claims.CloseEvidence(ctx, "cl-cl-winner", "ev-cl-b", thursday, note); err != nil {
				t.Fatalf("close: %v", err)
			}
			// Closing again, or closing a link that never existed, records nothing.
			if err := b.conn.Claims.CloseEvidence(ctx, "cl-cl-winner", "ev-cl-b", thursday.Add(time.Hour), note); err != nil {
				t.Fatalf("close again: %v", err)
			}
			if err := b.conn.Claims.CloseEvidence(ctx, "cl-cl-other", "ev-cl-a", thursday, note); err != nil {
				t.Fatalf("close absent: %v", err)
			}

			live, err := b.conn.Claims.ListEvidenceByClaimIDs(ctx, []string{"cl-cl-winner"})
			if err != nil || len(live) != 1 || live[0].EventID != "ev-cl-a" {
				t.Errorf("live evidence = %+v (%v), want only ev-cl-a", live, err)
			}
			all, err := b.conn.Claims.ListAllEvidence(ctx)
			if err != nil {
				t.Fatalf("all evidence: %v", err)
			}
			for _, l := range all {
				if l.EventID == "ev-cl-b" {
					t.Errorf("ListAllEvidence still returns the closed link %+v", l)
				}
			}
			holders, err := b.conn.Claims.ListByEventIDs(ctx, []string{"ev-cl-b"})
			if err != nil || len(holders) != 0 {
				t.Errorf("claims by the closed link's event = %+v (%v), want none", holders, err)
			}
			byClaimIDs, err := b.conn.Claims.ListClosedEvidenceByClaimIDs(ctx, []string{"cl-cl-winner", "cl-cl-other"})
			if err != nil {
				t.Fatalf("closed evidence by claim: %v", err)
			}
			byEventIDs, err := b.conn.Claims.ListClosedEvidenceByEventIDs(ctx, []string{"ev-cl-b", "ev-cl-a"})
			if err != nil {
				t.Fatalf("closed evidence by event: %v", err)
			}
			for _, closed := range [][]domain.ClosedEvidence{byClaimIDs, byEventIDs} {
				if len(closed) != 1 {
					t.Fatalf("closed evidence = %+v, want one link", closed)
				}
				c := closed[0]
				if c.ClaimID != "cl-cl-winner" || c.EventID != "ev-cl-b" || !c.CreatedAt.Equal(monday) ||
					!c.ClosedAt.Equal(thursday) || c.ClosedBy != "tester" || c.Reason != note.Reason {
					t.Errorf("closed link = %+v, want ev-cl-b recorded Monday, closed Thursday by tester", c)
				}
			}
			// A read at Tuesday still has it; a read after Thursday does not.
			for at, want := range map[time.Time]string{tuesday: "ev-cl-a,ev-cl-b", thursday.Add(time.Hour): "ev-cl-a"} {
				view := recorded.Claims{ClaimRepository: b.conn.Claims, Events: b.conn.Events, At: at}
				links, err := view.ListEvidenceByClaimIDs(ctx, []string{"cl-cl-winner"})
				if err != nil {
					t.Fatalf("recorded evidence: %v", err)
				}
				if got := eventIDsOf(links); got != want {
					t.Errorf("evidence recorded as of %s = %s, want %s", at.Format(time.DateOnly), got, want)
				}
				reached, err := view.ListByEventIDs(ctx, []string{"ev-cl-b"})
				if err != nil {
					t.Fatalf("recorded claims by event: %v", err)
				}
				if got := len(reached) == 1 && reached[0].ID == "cl-cl-winner"; got != at.Equal(tuesday) {
					t.Errorf("claims reached through ev-cl-b as of %s = %+v", at.Format(time.DateOnly), reached)
				}
			}

			// Edges: a self-loop and a duplicate the merge drops, one it moves.
			if err := b.conn.Relationships.Upsert(ctx, []domain.Relationship{
				{ID: "rel-cl-loop", Type: domain.RelationshipTypeSupports, FromClaimID: "cl-cl-loser", ToClaimID: "cl-cl-winner", CreatedAt: monday, CreatedBy: "tester"},
				{ID: "rel-cl-dup-w", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl-cl-other", ToClaimID: "cl-cl-winner", CreatedAt: monday, CreatedBy: "tester"},
				{ID: "rel-cl-dup-l", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl-cl-other", ToClaimID: "cl-cl-loser", CreatedAt: monday, CreatedBy: "tester"},
				{ID: "rel-cl-moved", Type: domain.RelationshipTypeSupports, FromClaimID: "cl-cl-loser", ToClaimID: "cl-cl-other", CreatedAt: monday, CreatedBy: "tester"},
			}); err != nil {
				t.Fatalf("edges: %v", err)
			}
			beforeMerge := time.Now().UTC()
			time.Sleep(5 * time.Millisecond)
			if err := b.conn.Relationships.RepointEndpoint(ctx, "cl-cl-loser", "cl-cl-winner"); err != nil {
				t.Fatalf("repoint: %v", err)
			}

			liveEdges, err := b.conn.Relationships.ListAll(ctx)
			if err != nil {
				t.Fatalf("all edges: %v", err)
			}
			liveIDs := edgeIDsOf(liveEdges)
			if n, err := b.conn.Relationships.CountAll(ctx); err != nil || n != int64(len(liveEdges)) {
				t.Errorf("CountAll = %d (%v), ListAll = %d: a closed edge leaked into the count", n, err, len(liveEdges))
			}
			if strings.Contains(liveIDs, "rel-cl-loop") || !strings.Contains(liveIDs, "rel-cl-moved") {
				t.Errorf("live edges after merge = %s, want the self-loop gone and the moved edge kept", liveIDs)
			}
			// One of the two contradictions collapsed; which one is the backend's choice.
			dupKept := strings.Count(liveIDs, "rel-cl-dup-")
			if dupKept != 1 {
				t.Errorf("live edges after merge = %s, want exactly one of the duplicate contradictions", liveIDs)
			}

			closedEdges, err := b.conn.Relationships.ListAllClosed(ctx)
			if err != nil {
				t.Fatalf("closed edges: %v", err)
			}
			byClaim, err := b.conn.Relationships.ListClosedByClaimIDs(ctx, []string{"cl-cl-loser"})
			if err != nil {
				t.Fatalf("closed edges by claim: %v", err)
			}
			if len(closedEdges) != 2 {
				t.Fatalf("closed edges = %+v, want the self-loop and one duplicate", closedEdges)
			}
			for _, c := range closedEdges {
				if strings.Contains(liveIDs, c.ID) {
					t.Errorf("edge %s is both live and closed", c.ID)
				}
				if c.Reason != domain.MergeClosedReason("cl-cl-loser", "cl-cl-winner") || c.ClosedAt.Before(beforeMerge) ||
					!c.CreatedAt.Equal(monday) || c.CreatedBy != "tester" {
					t.Errorf("closed edge = %+v, want closed by the merge with its original time and author", c)
				}
				if c.ID == "rel-cl-loop" && (c.FromClaimID != "cl-cl-loser" || c.ToClaimID != "cl-cl-winner") {
					t.Errorf("closed self-loop kept endpoints %s→%s, want the original loser→winner", c.FromClaimID, c.ToClaimID)
				}
			}
			if !strings.Contains(edgeIDsOf(closedRelsAsRels(byClaim)), "rel-cl-loop") {
				t.Errorf("closed edges touching the loser = %+v, want the self-loop", byClaim)
			}

			// A read before the merge sees all four edges.
			view := recorded.Relationships{RelationshipRepository: b.conn.Relationships, Claims: b.conn.Claims, At: beforeMerge}
			then, err := view.ListAll(ctx)
			if err != nil {
				t.Fatalf("recorded edges: %v", err)
			}
			if got := edgeIDsOf(then); got != "rel-cl-dup-l,rel-cl-dup-w,rel-cl-loop,rel-cl-moved" {
				t.Errorf("edges recorded before the merge = %s, want all four", got)
			}
			if after, err := (recorded.Relationships{RelationshipRepository: b.conn.Relationships, Claims: b.conn.Claims, At: time.Now().UTC().Add(time.Hour)}).ListAll(ctx); err != nil || edgeIDsOf(after) != liveIDs {
				t.Errorf("edges recorded after the merge = %s (%v), want the live set %s", edgeIDsOf(after), err, liveIDs)
			}

			// Destroying a claim or an event destroys its closed rows.
			if err := b.conn.Relationships.DeleteByClaim(ctx, "cl-cl-loser"); err != nil {
				t.Fatalf("delete edges of loser: %v", err)
			}
			if rest, err := b.conn.Relationships.ListClosedByClaimIDs(ctx, []string{"cl-cl-loser"}); err != nil || len(rest) != 0 {
				t.Errorf("closed edges of a deleted claim = %+v (%v), want none", rest, err)
			}
			if err := b.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: "cl-cl-other", EventID: "ev-cl-a"}}); err != nil {
				t.Fatalf("link other: %v", err)
			}
			if err := b.conn.Claims.CloseEvidence(ctx, "cl-cl-other", "ev-cl-a", thursday, note); err != nil {
				t.Fatalf("close other: %v", err)
			}
			if err := b.conn.Relationships.DeleteByClaim(ctx, "cl-cl-other"); err != nil {
				t.Fatalf("delete edges of other: %v", err)
			}
			if err := b.conn.Claims.DeleteCascade(ctx, "cl-cl-other"); err != nil {
				t.Fatalf("cascade: %v", err)
			}
			if rest, err := b.conn.Claims.ListClosedEvidenceByClaimIDs(ctx, []string{"cl-cl-other"}); err != nil || len(rest) != 0 {
				t.Errorf("closed links of a deleted claim = %+v (%v), want none", rest, err)
			}
			if err := b.conn.Events.DeleteByID(ctx, "ev-cl-b"); err != nil {
				t.Fatalf("delete event: %v", err)
			}
			if rest, err := b.conn.Claims.ListClosedEvidenceByClaimIDs(ctx, []string{"cl-cl-winner"}); err != nil || len(rest) != 0 {
				t.Errorf("closed links of a deleted event = %+v (%v), want none", rest, err)
			}
		})
	}
}

func eventIDsOf(links []domain.ClaimEvidence) string {
	ids := make([]string, len(links))
	for i, l := range links {
		ids[i] = l.EventID
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func edgeIDsOf(rels []domain.Relationship) string {
	ids := make([]string, len(rels))
	for i, r := range rels {
		ids[i] = r.ID
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func closedRelsAsRels(closed []domain.ClosedRelationship) []domain.Relationship {
	out := make([]domain.Relationship, len(closed))
	for i, c := range closed {
		out[i] = c.Association
	}
	return out
}
