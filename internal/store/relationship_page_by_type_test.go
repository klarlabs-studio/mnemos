package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// Every backend pages one relationship type newest first, id descending on
// ties, and counts the whole type. list_dissonances relies on this instead of
// decoding every association in the brain (ADR 0043); a backend without it
// falls back to that full read, so its absence is a failure here, not a skip.
func TestRelationships_PageByTypeAcrossBackends(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, b := range openBackends(t) {
		pager, ok := b.conn.Relationships.(ports.RelationshipTypePager)
		if !ok {
			t.Errorf("%s: relationship repository does not implement ports.RelationshipTypePager", b.name)
			continue
		}
		var claims []domain.Claim
		for i := 0; i < 8; i++ {
			claims = append(claims, domain.Claim{ID: fmt.Sprintf("pt%d", i), Text: "c", Type: domain.ClaimTypeFact,
				Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: base})
		}
		if err := b.conn.Claims.Upsert(ctx, claims); err != nil {
			t.Fatalf("%s: %v", b.name, err)
		}
		at := func(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }
		edges := []domain.Relationship{
			{ID: "pt-c1", Type: domain.RelationshipTypeContradicts, FromClaimID: "pt0", ToClaimID: "pt1", CreatedAt: at(1)},
			{ID: "pt-c3", Type: domain.RelationshipTypeContradicts, FromClaimID: "pt2", ToClaimID: "pt3", CreatedAt: at(3)},
			{ID: "pt-c2a", Type: domain.RelationshipTypeContradicts, FromClaimID: "pt4", ToClaimID: "pt5", CreatedAt: at(2)},
			{ID: "pt-c2b", Type: domain.RelationshipTypeContradicts, FromClaimID: "pt6", ToClaimID: "pt7", CreatedAt: at(2)},
			{ID: "pt-s9", Type: domain.RelationshipTypeSupports, FromClaimID: "pt0", ToClaimID: "pt2", CreatedAt: at(9)},
		}
		if err := b.conn.Relationships.Upsert(ctx, edges); err != nil {
			t.Fatalf("%s: %v", b.name, err)
		}
		ids := func(rs []domain.Relationship) string {
			out := make([]string, len(rs))
			for i, r := range rs {
				out[i] = r.ID
			}
			return strings.Join(out, " ")
		}
		for _, tc := range []struct {
			limit, offset int
			want          string
		}{
			{10, 0, "pt-c3 pt-c2b pt-c2a pt-c1"},
			{2, 0, "pt-c3 pt-c2b"},
			{2, 2, "pt-c2a pt-c1"},
			{2, 3, "pt-c1"},
			{2, 4, ""},
		} {
			got, total, err := pager.PageByType(ctx, domain.RelationshipTypeContradicts, tc.limit, tc.offset)
			if err != nil {
				t.Fatalf("%s: PageByType(%d, %d): %v", b.name, tc.limit, tc.offset, err)
			}
			if total != 4 {
				t.Errorf("%s: PageByType(%d, %d) total = %d, want 4", b.name, tc.limit, tc.offset, total)
			}
			if ids(got) != tc.want {
				t.Errorf("%s: PageByType(%d, %d) = [%s], want [%s]", b.name, tc.limit, tc.offset, ids(got), tc.want)
			}
			for _, r := range got {
				if r.FromClaimID == "" || r.ToClaimID == "" || r.CreatedAt.IsZero() || r.Type != domain.RelationshipTypeContradicts {
					t.Errorf("%s: incomplete row %+v", b.name, r)
				}
			}
		}
	}
}
