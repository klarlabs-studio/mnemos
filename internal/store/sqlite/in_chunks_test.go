package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// More ids than SQLite binds in one statement (32766). Every *ByIDs read must
// answer, not fail: the relate candidate fetch on a 100k-belief brain asks for
// ~50k claims, and its failure was swallowed by the write path, which then
// related new claims against nothing.
func TestByIDReads_AcceptMoreIDsThanSQLiteBindsAtOnce(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	claims := NewClaimRepository(db)
	events := NewEventRepository(db)
	rels := NewRelationshipRepository(db)
	now := time.Now().UTC()

	// 40k ids, only a few of them stored, placed so they fall in different
	// chunks: the first, one in the middle, the last.
	const n = 40000
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("x%05d", i)
	}
	stored := []string{ids[0], ids[n/2], ids[n-1]}
	for _, id := range stored {
		if err := events.Append(ctx, domain.Event{ID: "ev-" + id, Content: "e", SchemaVersion: "v1",
			SourceInputID: "src-" + id, Timestamp: now, IngestedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := claims.Upsert(ctx, []domain.Claim{{ID: id, Text: "c " + id, Type: domain.ClaimTypeFact,
			Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: now}}); err != nil {
			t.Fatal(err)
		}
		if err := claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: id, EventID: "ev-" + id}}); err != nil {
			t.Fatal(err)
		}
	}
	// One edge whose endpoints sit in different chunks: it matches twice and
	// must be returned once.
	if err := rels.Upsert(ctx, []domain.Relationship{{ID: "edge", Type: domain.RelationshipTypeSupports,
		FromClaimID: ids[0], ToClaimID: ids[n-1], CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	eventIDs := make([]string, n)
	for i, id := range ids {
		eventIDs[i] = "ev-" + id
	}

	if got, err := claims.ListByIDs(ctx, ids); err != nil || len(got) != 3 {
		t.Errorf("claims.ListByIDs: %d claims, %v; want 3", len(got), err)
	}
	if got, err := claims.ListByEventIDs(ctx, eventIDs); err != nil || len(got) != 3 {
		t.Errorf("claims.ListByEventIDs: %d claims, %v; want 3", len(got), err)
	}
	if got, err := claims.ListEvidenceByClaimIDs(ctx, ids); err != nil || len(got) != 3 {
		t.Errorf("claims.ListEvidenceByClaimIDs: %d links, %v; want 3", len(got), err)
	}
	if got, err := events.ListByIDs(ctx, eventIDs); err != nil || len(got) != 3 {
		t.Errorf("events.ListByIDs: %d events, %v; want 3", len(got), err)
	}
	if got, err := rels.ListByClaimIDs(ctx, ids); err != nil || len(got) != 1 {
		t.Errorf("relationships.ListByClaimIDs: %d edges, %v; want the 1 edge once", len(got), err)
	}
}
