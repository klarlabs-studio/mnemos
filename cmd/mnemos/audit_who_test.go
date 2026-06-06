package main

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// seedAuditFixture writes a small mixed history attributed to two
// distinct principals so the filter tests have something to discriminate.
func seedAuditFixture(t *testing.T, conn *store.Conn, now time.Time) {
	t.Helper()
	ctx := context.Background()
	rfc := func(o time.Duration) time.Time { return now.Add(o).UTC() }

	// Two events: alice owns one, bob owns one.
	seedEventConnAs(t, conn, "ev_alice", "r", "alice's event", "in1", `{}`, rfc(0), "usr_alice")
	seedEventConnAs(t, conn, "ev_bob", "r", "bob's event", "in2", `{}`, rfc(time.Minute), "usr_bob")

	// Two claims, same split.
	seedClaimConnAs(t, conn, "cl_alice", "alice's claim", "fact", "active", 0.8, rfc(0), "usr_alice")
	seedClaimConnAs(t, conn, "cl_bob", "bob's claim", "fact", "active", 0.8, rfc(time.Minute), "usr_bob")

	// One relationship from alice.
	seedRelationshipConnAs(t, conn, "rel_alice", "supports", "cl_alice", "cl_bob", rfc(2*time.Minute), "usr_alice")

	// One embedding from bob.
	if err := conn.Embeddings.Upsert(ctx, "ev_bob", "event", []float32{0.1, 0.2}, "test-model", "usr_bob"); err != nil {
		t.Fatalf("upsert embedding: %v", err)
	}

	// One status transition by alice — upsert the claim with a new
	// status through the port so the history row is recorded naturally.
	aliceClaim := domain.Claim{
		ID:         "cl_alice",
		Text:       "alice's claim",
		Type:       domain.ClaimTypeFact,
		Status:     domain.ClaimStatusContested,
		Confidence: 0.8,
		CreatedAt:  rfc(0),
		CreatedBy:  "usr_alice",
	}
	if err := conn.Claims.UpsertWithReasonAs(ctx, []domain.Claim{aliceClaim}, "test", "usr_alice"); err != nil {
		t.Fatalf("upsert claim for transition: %v", err)
	}
}

func TestBuildAuditWhoExport_FiltersByPrincipal(t *testing.T) {
	_, conn := openTestStore(t)
	seedAuditFixture(t, conn, time.Now().UTC().Add(-time.Hour))

	alice, err := buildAuditWhoExport(context.Background(), conn, "usr_alice", time.Time{})
	if err != nil {
		t.Fatalf("alice: %v", err)
	}
	if alice.Counts.Events != 1 || alice.Counts.Claims != 1 ||
		alice.Counts.Relationships != 1 || alice.Counts.Embeddings != 0 ||
		alice.Counts.Transitions != 1 {
		t.Errorf("alice counts wrong: %+v", alice.Counts)
	}
	if len(alice.Events) != 1 || alice.Events[0].ID != "ev_alice" {
		t.Errorf("alice event content wrong: %+v", alice.Events)
	}

	bob, err := buildAuditWhoExport(context.Background(), conn, "usr_bob", time.Time{})
	if err != nil {
		t.Fatalf("bob: %v", err)
	}
	if bob.Counts.Events != 1 || bob.Counts.Claims != 1 ||
		bob.Counts.Relationships != 0 || bob.Counts.Embeddings != 1 ||
		bob.Counts.Transitions != 0 {
		t.Errorf("bob counts wrong: %+v", bob.Counts)
	}
}

func TestBuildAuditWhoExport_SinceFilterPrunesOlderRows(t *testing.T) {
	_, conn := openTestStore(t)

	old := time.Now().UTC().Add(-48 * time.Hour)
	seedAuditFixture(t, conn, old)

	// since=24h ago → all the seeded rows are older than that, so
	// alice's report should be empty (zero counts) but well-formed.
	since := time.Now().UTC().Add(-24 * time.Hour)
	exp, err := buildAuditWhoExport(context.Background(), conn, "usr_alice", since)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	// Events, claims, and relationships are pruned (48h old vs 24h
	// since). The status transition is recorded at Upsert time (now),
	// so it falls after the since window and correctly remains.
	if exp.Counts.Events != 0 || exp.Counts.Claims != 0 ||
		exp.Counts.Relationships != 0 {
		t.Errorf("since filter didn't prune everything: %+v", exp.Counts)
	}
	if exp.Since == "" {
		t.Errorf("Since timestamp should be set on export")
	}
	// Conversely, no since filter returns the full alice slice.
	full, err := buildAuditWhoExport(context.Background(), conn, "usr_alice", time.Time{})
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if full.Counts.Events != 1 || full.Counts.Claims != 1 || full.Counts.Transitions != 1 {
		t.Errorf("no-since alice counts wrong: %+v", full.Counts)
	}
}

func TestBuildAuditWhoExport_UnknownPrincipalIsEmpty(t *testing.T) {
	_, conn := openTestStore(t)

	exp, err := buildAuditWhoExport(context.Background(), conn, "usr_nonexistent", time.Time{})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if exp.Counts.Events != 0 || exp.Counts.Claims != 0 ||
		exp.Counts.Relationships != 0 || exp.Counts.Embeddings != 0 ||
		exp.Counts.Transitions != 0 {
		t.Errorf("expected empty report for unknown principal, got %+v", exp.Counts)
	}
	if exp.Principal != "usr_nonexistent" {
		t.Errorf("Principal field not echoed back: %q", exp.Principal)
	}
}

func TestBuildAuditWhoExport_SystemSentinelMatches(t *testing.T) {
	_, conn := openTestStore(t)

	// Seed a row with the explicit <system> created_by — pipeline
	// writes use this when no real actor is configured.
	now := time.Now().UTC()
	seedEventConnAs(t, conn, "ev_sys", "r", "system event", "in", `{}`, now, domain.SystemUser)

	exp, err := buildAuditWhoExport(context.Background(), conn, domain.SystemUser, time.Time{})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if exp.Counts.Events != 1 {
		t.Errorf("expected 1 system event, got %d", exp.Counts.Events)
	}
}
