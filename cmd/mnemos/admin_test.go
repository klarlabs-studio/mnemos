package main

import (
	"context"
	"testing"
	"time"
)

func TestReset_WipesEverything(t *testing.T) {
	_, conn := openTestStore(t)
	w := wrapTestWriter(t, conn)

	now := time.Now().UTC()
	seedEventConn(t, conn, "ev1", "r", "content", "in1", `{}`, now)
	seedClaimConn(t, conn, "cl1", "claim 1", "fact", "active", 0.8, now)
	seedClaimConn(t, conn, "cl2", "claim 2", "fact", "active", 0.7, now)
	seedRelationshipConn(t, conn, "rl1", "supports", "cl1", "cl2", now)

	ctx := context.Background()
	counts, err := w.Reset(ctx, false)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if counts.Claims != 2 || counts.Relationships != 1 || counts.Events != 1 {
		t.Fatalf("counts not captured pre-delete: %+v", counts)
	}

	gotClaims, _ := conn.Claims.ListAll(ctx)
	if len(gotClaims) != 0 {
		t.Fatalf("claims not deleted: %d remaining", len(gotClaims))
	}
	gotEvents, _ := conn.Events.ListAll(ctx)
	if len(gotEvents) != 0 {
		t.Fatalf("events not deleted: %d remaining", len(gotEvents))
	}
}

func TestReset_KeepEvents(t *testing.T) {
	_, conn := openTestStore(t)
	w := wrapTestWriter(t, conn)

	now := time.Now().UTC()
	seedEventConn(t, conn, "ev1", "r", "content", "in1", `{}`, now)
	seedClaimConn(t, conn, "cl1", "x", "fact", "active", 0.8, now)

	ctx := context.Background()
	if _, err := w.Reset(ctx, true); err != nil {
		t.Fatalf("reset: %v", err)
	}

	gotEvents, _ := conn.Events.ListAll(ctx)
	if len(gotEvents) != 1 {
		t.Fatalf("events should be kept, got %d", len(gotEvents))
	}
	gotClaims, _ := conn.Claims.ListAll(ctx)
	if len(gotClaims) != 0 {
		t.Fatalf("claims should be deleted, got %d", len(gotClaims))
	}
}

// TestDeleteClaimCascade_RemovesDerivedRows exercises the governed
// cascade that handleDeleteClaim now performs: a single
// DeleteClaimCascade action drops relationships -> embedding -> claim
// cascade. Routed through the governed Writer so the assertion stays
// focused on storage semantics while still going through the kernel.
func TestDeleteClaimCascade_RemovesDerivedRows(t *testing.T) {
	_, conn := openTestStore(t)
	w := wrapTestWriter(t, conn)

	now := time.Now().UTC()
	seedEventConn(t, conn, "ev1", "r", "content", "in1", `{}`, now)
	seedClaimConn(t, conn, "cl1", "doomed", "fact", "active", 0.8, now)
	seedClaimConn(t, conn, "cl2", "neighbor", "fact", "active", 0.8, now)
	seedRelationshipConn(t, conn, "rl1", "supports", "cl1", "cl2", now)

	ctx := context.Background()
	if err := w.DeleteClaimCascade(ctx, "cl1"); err != nil {
		t.Fatalf("delete claim cascade: %v", err)
	}

	claims, _ := conn.Claims.ListAll(ctx)
	if len(claims) != 1 || claims[0].ID != "cl2" {
		t.Fatalf("expected cl2 to remain, got %+v", claims)
	}
	rels, _ := conn.Relationships.ListByClaim(ctx, "cl2")
	if len(rels) != 0 {
		t.Fatalf("relationships referencing cl1 should be gone, got %d", len(rels))
	}
}

// TestListIDsMissingEmbedding covers the anti-join port path used by
// `mnemos reembed` to scope work to claims without an embedding.
func TestListIDsMissingEmbedding(t *testing.T) {
	_, conn := openTestStore(t)

	now := time.Now().UTC()
	seedClaimConn(t, conn, "with", "has embedding", "fact", "active", 0.8, now)
	seedClaimConn(t, conn, "without", "no embedding", "fact", "active", 0.8, now)

	ctx := context.Background()
	if err := conn.Embeddings.Upsert(ctx, "with", "claim", []float32{0, 0, 0}, "m", ""); err != nil {
		t.Fatalf("seed embedding: %v", err)
	}

	missing, err := conn.Claims.ListIDsMissingEmbedding(ctx)
	if err != nil {
		t.Fatalf("list missing: %v", err)
	}
	if len(missing) != 1 || missing[0] != "without" {
		t.Fatalf("expected [without], got %v", missing)
	}
}
