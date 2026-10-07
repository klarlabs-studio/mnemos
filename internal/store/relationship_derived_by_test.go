package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// The rule set that inferred an edge survives storage on every backend and
// every read path, and re-deriving the edge under a newer rule set records the
// new one. Without this, edges an old rule set wrote cannot be told apart from
// current ones, and a rule change can only be applied by re-deriving
// everything.
func TestRelationships_DerivedByRoundTripsAcrossBackends(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, b := range openBackends(t) {
		var claims []domain.Claim
		for i := 0; i < 3; i++ {
			claims = append(claims, domain.Claim{ID: fmt.Sprintf("db%d", i), Text: "c", Type: domain.ClaimTypeFact,
				Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: at})
		}
		if err := b.conn.Claims.Upsert(ctx, claims); err != nil {
			t.Fatal(err)
		}
		edges := []domain.Relationship{
			{ID: "dr-rule", Type: domain.RelationshipTypeSupports, FromClaimID: "db0", ToClaimID: "db1", CreatedAt: at, DerivedBy: "relate/v3"},
			{ID: "dr-llm", Type: domain.RelationshipTypeContradicts, FromClaimID: "db1", ToClaimID: "db2", CreatedAt: at, DerivedBy: "relate-llm-causal/v1"},
			{ID: "dr-explicit", Type: domain.RelationshipTypeSupports, FromClaimID: "db0", ToClaimID: "db2", CreatedAt: at},
		}
		if err := b.conn.Relationships.Upsert(ctx, edges); err != nil {
			t.Fatalf("%s: %v", b.name, err)
		}
		want := map[string]string{"dr-rule": "relate/v3", "dr-llm": "relate-llm-causal/v1", "dr-explicit": ""}
		check := func(label string, got []domain.Relationship, wanted map[string]string) {
			t.Helper()
			n := 0
			for _, r := range got {
				w, ok := wanted[r.ID]
				if !ok {
					continue
				}
				n++
				if r.DerivedBy != w {
					t.Errorf("%s %s: %s DerivedBy %q, want %q", b.name, label, r.ID, r.DerivedBy, w)
				}
			}
			if n == 0 {
				t.Errorf("%s %s: no edges read back", b.name, label)
			}
		}
		all, err := b.conn.Relationships.ListAll(ctx)
		if err != nil {
			t.Fatal(err)
		}
		check("ListAll", all, want)
		byClaim, err := b.conn.Relationships.ListByClaim(ctx, "db1")
		if err != nil {
			t.Fatal(err)
		}
		check("ListByClaim", byClaim, want)
		byIDs, err := b.conn.Relationships.ListByClaimIDs(ctx, []string{"db0", "db2"})
		if err != nil {
			t.Fatal(err)
		}
		check("ListByClaimIDs", byIDs, want)
		if l, ok := b.conn.Relationships.(ports.RelationshipTypeLister); ok {
			typed, err := l.ListByType(ctx, domain.RelationshipTypeContradicts)
			if err != nil {
				t.Fatal(err)
			}
			check("ListByType", typed, want)
		}
		if p, ok := b.conn.Relationships.(ports.RelationshipPager); ok {
			pg, err := p.PageRelationships(ctx, "", "", 100)
			if err != nil {
				t.Fatal(err)
			}
			check("PageRelationships", pg.Items, want)
		}

		// Re-derived under a newer rule set: the stamp follows.
		edges[0].DerivedBy = "relate/v4"
		if err := b.conn.Relationships.Upsert(ctx, edges[:1]); err != nil {
			t.Fatal(err)
		}
		after, err := b.conn.Relationships.ListByClaim(ctx, "db0")
		if err != nil {
			t.Fatal(err)
		}
		check("after re-derivation", after, map[string]string{"dr-rule": "relate/v4"})
	}
}
