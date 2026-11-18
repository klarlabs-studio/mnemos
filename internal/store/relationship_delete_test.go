package store_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/recorded"
)

// CloseByIDs closes exactly the named edges on every backend, across more than
// one statement chunk, ignores unknown ids, and reports the rows it closed. The
// closed edges leave the live reads and come back from the closed listers with
// the closure's time and note (ADR 0036 §8).
func TestRelationships_CloseByIDsAcrossBackends(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	const edges = 1203 // spans three 500-id chunks
	for _, b := range openBackends(t) {
		del, ok := b.conn.Relationships.(ports.RelationshipCloser)
		if !ok {
			t.Fatalf("%s: relationship repository does not implement RelationshipCloser", b.name)
		}
		claims := make([]domain.Claim, 0, edges+1)
		for i := 0; i <= edges; i++ {
			claims = append(claims, domain.Claim{ID: fmt.Sprintf("c%05d", i), Text: fmt.Sprintf("claim %d", i),
				Type: domain.ClaimTypeFact, Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: at})
		}
		if err := b.conn.Claims.Upsert(ctx, claims); err != nil {
			t.Fatalf("%s: upsert claims: %v", b.name, err)
		}
		rels := make([]domain.Relationship, 0, edges)
		for i := 1; i <= edges; i++ {
			rels = append(rels, domain.Relationship{ID: fmt.Sprintf("r%05d", i), Type: domain.RelationshipTypeSupports,
				FromClaimID: "c00000", ToClaimID: fmt.Sprintf("c%05d", i), CreatedAt: at})
		}
		if err := b.conn.Relationships.Upsert(ctx, rels); err != nil {
			t.Fatalf("%s: upsert relationships: %v", b.name, err)
		}

		var drop []string
		for i := 1; i <= edges; i++ {
			if i%3 != 0 { // keep every third edge
				drop = append(drop, fmt.Sprintf("r%05d", i))
			}
		}
		closedAt := at.Add(48 * time.Hour)
		note := domain.ChangeNote{Reason: "pruned: test", ChangedBy: "pruner"}
		n, err := del.CloseByIDs(ctx, append(slices.Clone(drop), "no-such-edge"), closedAt, note)
		if err != nil {
			t.Fatalf("%s: CloseByIDs: %v", b.name, err)
		}
		if n != int64(len(drop)) {
			t.Errorf("%s: CloseByIDs reported %d rows, want %d", b.name, n, len(drop))
		}
		closed, err := b.conn.Relationships.ListAllClosed(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(closed) != len(drop) {
			t.Errorf("%s: %d closed edges, want %d", b.name, len(closed), len(drop))
		}
		for _, c := range closed {
			if !c.ClosedAt.Equal(closedAt) || c.ClosedBy != "pruner" || c.Reason != note.Reason || !c.CreatedAt.Equal(at) {
				t.Errorf("%s: closed edge %+v, want closed at %v by pruner with its original time", b.name, c, closedAt)
				break
			}
		}
		left, err := b.conn.Relationships.ListByClaim(ctx, "c00000")
		if err != nil {
			t.Fatal(err)
		}
		if len(left) != edges/3 {
			t.Errorf("%s: %d edges left, want %d", b.name, len(left), edges/3)
		}
		for _, r := range left {
			var i int
			_, _ = fmt.Sscanf(r.ID, "r%05d", &i)
			if i%3 != 0 {
				t.Errorf("%s: edge %s should have been closed", b.name, r.ID)
			}
		}
		// A read before the prune still has every edge; a read after it has
		// what is live.
		for when, want := range map[time.Time]int{closedAt.Add(-time.Hour): edges, closedAt.Add(time.Hour): edges / 3} {
			view := recorded.Relationships{RelationshipRepository: b.conn.Relationships, Claims: b.conn.Claims, At: when}
			got, err := view.ListByClaimIDs(ctx, []string{"c00000"})
			if err != nil {
				t.Fatalf("%s: recorded edges: %v", b.name, err)
			}
			if len(got) != want {
				t.Errorf("%s: %d edges recorded as of %s, want %d", b.name, len(got), when, want)
			}
		}
		if n, err := del.CloseByIDs(ctx, nil, closedAt, note); err != nil || n != 0 {
			t.Errorf("%s: CloseByIDs(nil) = %d, %v", b.name, n, err)
		}
	}
}
