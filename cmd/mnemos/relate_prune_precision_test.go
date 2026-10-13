package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/relate"
)

// `relate --prune-stale` against a temp brain seeded with the noise a real
// brain held before relate/v4: contradiction edges written by older detectors
// (empty derived_by, base strength) between session headers, code lines,
// fragments, agreeing paraphrases and unrelated claims, plus a hub claim with
// more genuine contradictions than the cap allows. The pass must drop all of
// the noise, keep the genuine conflicts, and hold the hub to the cap.
func TestPruneStale_DropsPreV4NoiseAndCapsHubs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// Never let this test resolve to a real brain.
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+dir+"/brain.db")

	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var claims []domain.Claim
	add := func(id, text string) {
		claims = append(claims, domain.Claim{ID: id, Text: text, Type: domain.ClaimTypeFact,
			Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: at})
	}
	var rels []domain.Relationship
	edge := func(id, from, to string) {
		rels = append(rels, domain.Relationship{ID: id, Type: domain.RelationshipTypeContradicts,
			FromClaimID: from, ToClaimID: to, CreatedAt: at})
	}

	// Noise, one edge per class of the hand-classified sample.
	add("hdr", "User messages in this session:")
	add("fact1", "User messages are not persisted in this session")
	edge("n-header", "hdr", "fact1")
	add("code", "type CompilationMachine interface {")
	add("fact2", "CompilationMachine is not an interface")
	edge("n-code", "code", "fact2")
	add("frag", "The gate passed")
	add("fact3", "The gate did not pass on the first push")
	edge("n-fragment", "frag", "fact3")
	add("para1", "Tests fail without the fix and pass with it")
	add("para2", "The fix makes the test go red→green")
	edge("n-paraphrase", "para1", "para2")
	add("unrel1", "Release build is still running")
	add("unrel2", "The release notes were finished yesterday")
	edge("n-unrelated", "unrel1", "unrel2")

	// Genuine conflicts.
	add("flag-on", "Feature X is enabled in prod")
	add("flag-off", "Feature X is disabled in prod")
	edge("g-flag", "flag-on", "flag-off")
	// A hub: eight genuine contradictions on one claim, three over the cap.
	add("pg15", "The deploy uses Postgres 15")
	for v := 16; v < 16+relate.MaxContradictionsPerClaim+3; v++ {
		id := fmt.Sprintf("pg%d", v)
		add(id, fmt.Sprintf("The deploy uses Postgres %d", v))
		edge("g-"+id, "pg15", id)
	}

	conn, err := openConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Claims.Upsert(ctx, claims); err != nil {
		t.Fatal(err)
	}
	if err := conn.Relationships.Upsert(ctx, rels); err != nil {
		t.Fatal(err)
	}
	closeConn(conn)

	pruneStaleRelationships(false, Flags{})

	conn, err = openConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer closeConn(conn)
	left, err := conn.Relationships.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	hub := 0
	for _, r := range left {
		got[r.ID] = true
		if r.FromClaimID == "pg15" || r.ToClaimID == "pg15" {
			hub++
		}
	}
	for _, id := range []string{"n-header", "n-code", "n-fragment", "n-paraphrase", "n-unrelated"} {
		if got[id] {
			t.Errorf("noise edge %s survived --prune-stale", id)
		}
	}
	if !got["g-flag"] {
		t.Error("genuine contradiction g-flag was pruned")
	}
	if hub != relate.MaxContradictionsPerClaim {
		t.Errorf("hub kept %d contradictions, want the cap of %d", hub, relate.MaxContradictionsPerClaim)
	}
	if want := 1 + relate.MaxContradictionsPerClaim; len(left) != want {
		t.Errorf("%d edges left, want %d", len(left), want)
	}
}
