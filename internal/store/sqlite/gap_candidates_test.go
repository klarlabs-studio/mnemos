package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// The by-id and by-scan routes of GapCandidates return the same candidates:
// which one runs depends only on how many there are.
func TestGapCandidates_RoutesAgree(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	claims := NewClaimRepository(db)
	rels := NewRelationshipRepository(db)
	now := time.Now().UTC()
	var cs []domain.Claim
	for i := 0; i < 60; i++ {
		c := domain.Claim{ID: fmt.Sprintf("c%02d", i), Text: fmt.Sprintf("claim %d", i), Type: domain.ClaimTypeFact,
			Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: now}
		if i%7 == 0 {
			c.Type = domain.ClaimTypeHypothesis
		}
		if i%13 == 0 {
			c.Status = domain.ClaimStatusDeprecated
		}
		cs = append(cs, c)
	}
	if err := claims.Upsert(ctx, cs); err != nil {
		t.Fatal(err)
	}
	// Closed validity goes through SetValidity; Upsert does not write it.
	for i := 0; i < 60; i += 11 {
		if err := claims.SetValidity(ctx, fmt.Sprintf("c%02d", i), now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	var links []domain.ClaimEvidence
	for i := 0; i < 60; i += 2 {
		for k := 0; k <= i%3; k++ {
			links = append(links, domain.ClaimEvidence{ClaimID: fmt.Sprintf("c%02d", i), EventID: fmt.Sprintf("e%02d-%d", i, k)})
		}
	}
	if err := claims.UpsertEvidence(ctx, links); err != nil {
		t.Fatal(err)
	}
	var rs []domain.Relationship
	for i := 0; i < 40; i++ {
		rs = append(rs, domain.Relationship{ID: fmt.Sprintf("r%02d", i), Type: domain.RelationshipTypeContradicts,
			FromClaimID: fmt.Sprintf("c%02d", i%15), ToClaimID: fmt.Sprintf("c%02d", 20+i%9), CreatedAt: now})
	}
	if err := rels.Upsert(ctx, rs); err != nil {
		t.Fatal(err)
	}

	route := func(fraction float64) ([]string, map[string]int, map[string]int, int) {
		prev := gapScanFraction
		gapScanFraction = fraction
		defer func() { gapScanFraction = prev }()
		got, err := claims.GapCandidates(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, len(got.Claims))
		for i, c := range got.Claims {
			ids[i] = c.ID
		}
		sort.Strings(ids)
		return ids, got.Contradictions, got.Evidence, got.OpenClaims
	}
	byID, contraA, evA, openA := route(1e9)
	byScan, contraB, evB, openB := route(0)
	if len(byID) == 0 || len(evA) == 0 {
		t.Fatal("no candidates or no evidence; the comparison is vacuous")
	}
	if !reflect.DeepEqual(byID, byScan) || !reflect.DeepEqual(contraA, contraB) || !reflect.DeepEqual(evA, evB) || openA != openB {
		t.Fatalf("routes disagree:\nby id:   %v %v %v %d\nby scan: %v %v %v %d", byID, contraA, evA, openA, byScan, contraB, evB, openB)
	}
}
