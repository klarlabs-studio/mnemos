package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// Every backend that serves brain health from the store counts dangling edges
// exactly: one per relationship with a missing endpoint, however many of its
// endpoints are missing and however many edges share a missing id. SQLite's
// foreign keys refuse such an edge, so a brain only holds one from a write made
// with them off (legacy data, a libSQL connection); the seed does the same.
// The count is reported as referential corruption, so it must never be
// estimated (#438 made it cheap, not approximate).
func TestHealthSampler_CountDanglingRelationshipsIsExact(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	edge := func(id, typ, from, to string) domain.Relationship {
		return domain.Relationship{ID: id, Type: domain.RelationshipType(typ), FromClaimID: from, ToClaimID: to, CreatedAt: at}
	}
	sound := []domain.Relationship{
		edge("ok-1", "supports", "dg-a", "dg-b"),
		edge("ok-2", "contradicts", "dg-b", "dg-c"),
		edge("ok-3", "supports", "dg-c", "dg-a"),
	}
	dangling := []domain.Relationship{
		edge("bad-to", "supports", "dg-a", "ghost-1"),
		edge("bad-to-again", "contradicts", "dg-b", "ghost-1"), // a missing id shared by two edges counts twice
		edge("bad-from", "supports", "ghost-2", "dg-c"),
		edge("bad-both", "supports", "ghost-3", "ghost-4"),  // both endpoints missing counts once
		edge("bad-cross", "contradicts", "ghost-1", "dg-a"), // missing as a target elsewhere, a source here
		edge("bad-deleted", "supports", "dg-c", "dg-gone"),  // endpoint deleted after the edge was written
	}
	for _, b := range openBackends(t) {
		sampler, ok := b.conn.Claims.(ports.HealthSampler)
		if !ok {
			t.Logf("%s: no HealthSampler; brain health takes the exact in-process path there", b.name)
			continue
		}
		var claims []domain.Claim
		for _, id := range []string{"dg-a", "dg-b", "dg-c", "dg-gone"} {
			claims = append(claims, domain.Claim{ID: id, Text: "belief " + id, Type: domain.ClaimTypeFact,
				Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: at})
		}
		if err := b.conn.Claims.Upsert(ctx, claims); err != nil {
			t.Fatalf("%s: seed claims: %v", b.name, err)
		}
		if err := b.conn.Relationships.Upsert(ctx, sound); err != nil {
			t.Fatalf("%s: seed sound edges: %v", b.name, err)
		}
		if n, err := sampler.CountDanglingRelationships(ctx); err != nil || n != 0 {
			t.Fatalf("%s: CountDanglingRelationships on a sound brain = %d, %v; want 0", b.name, n, err)
		}
		seedDanglingEdges(t, b, dangling)
		n, err := sampler.CountDanglingRelationships(ctx)
		if err != nil {
			t.Fatalf("%s: CountDanglingRelationships: %v", b.name, err)
		}
		if n != len(dangling) {
			t.Errorf("%s: CountDanglingRelationships = %d, want %d", b.name, n, len(dangling))
		}
	}
}

// seedDanglingEdges writes edges whose endpoints may be missing, then removes
// dg-gone so an edge written while it existed dangles too. SQLite needs its
// foreign keys off for both, on one pinned connection.
func seedDanglingEdges(t *testing.T, b backendConn, rels []domain.Relationship) {
	t.Helper()
	ctx := context.Background()
	db, ok := b.conn.Raw.(*sql.DB)
	if !ok {
		// The in-process store has no foreign keys. Its delete cascades to
		// the claim's edges, so the edge to dg-gone is written after it.
		if err := b.conn.Claims.DeleteCascade(ctx, "dg-gone"); err != nil {
			t.Fatalf("%s: delete dg-gone: %v", b.name, err)
		}
		if err := b.conn.Relationships.Upsert(ctx, rels); err != nil {
			t.Fatalf("%s: seed dangling edges: %v", b.name, err)
		}
		return
	}
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("%s: pin connection: %v", b.name, err)
	}
	defer func() { _ = c.Close() }()
	stmts := []string{`PRAGMA foreign_keys = OFF`}
	for range rels {
		stmts = append(stmts, `INSERT INTO relationships (id, type, from_claim_id, to_claim_id, created_at) VALUES (?, ?, ?, ?, ?)`)
	}
	stmts = append(stmts, `DELETE FROM claims WHERE id = 'dg-gone'`, `PRAGMA foreign_keys = ON`)
	for i, s := range stmts {
		var args []any
		if i >= 1 && i <= len(rels) {
			r := rels[i-1]
			args = []any{r.ID, string(r.Type), r.FromClaimID, r.ToClaimID, r.CreatedAt.Format(time.RFC3339Nano)}
		}
		if _, err := c.ExecContext(ctx, s, args...); err != nil {
			t.Fatalf("%s: %s: %v", b.name, s, err)
		}
	}
}
