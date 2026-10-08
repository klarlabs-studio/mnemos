package store_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// DeleteByIDs removes exactly the named edges on every backend, across more
// than one statement chunk, ignores unknown ids, and reports the rows it removed.
func TestRelationships_DeleteByIDsAcrossBackends(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	const edges = 1203 // spans three 500-id chunks
	for _, b := range openBackends(t) {
		del, ok := b.conn.Relationships.(ports.RelationshipDeleter)
		if !ok {
			t.Fatalf("%s: relationship repository does not implement RelationshipDeleter", b.name)
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
		n, err := del.DeleteByIDs(ctx, append(slices.Clone(drop), "no-such-edge"))
		if err != nil {
			t.Fatalf("%s: DeleteByIDs: %v", b.name, err)
		}
		if n != int64(len(drop)) {
			t.Errorf("%s: DeleteByIDs reported %d rows, want %d", b.name, n, len(drop))
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
				t.Errorf("%s: edge %s should have been deleted", b.name, r.ID)
			}
		}
		if n, err := del.DeleteByIDs(ctx, nil); err != nil || n != 0 {
			t.Errorf("%s: DeleteByIDs(nil) = %d, %v", b.name, n, err)
		}
	}
}
