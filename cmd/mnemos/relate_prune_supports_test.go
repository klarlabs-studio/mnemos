package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// A hub claim with 30 supports edges of graded strength and one contradiction:
// the dry run deletes nothing, --apply keeps the top-k strongest supports and
// the contradiction, and a second --apply finds nothing left to do.
func TestPruneSupports_DryRunThenApply(t *testing.T) {
	ctx := context.Background()
	conn := newServerTestStore_conn(t)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	hubWords := []string{"ledger", "invoice", "refund", "currency", "settlement", "audit", "payout", "reconcile"}
	claims := []domain.Claim{{ID: "hub", Text: strings.Join(hubWords, " "), Type: domain.ClaimTypeFact,
		Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: at}}
	var rels []domain.Relationship
	for i := 0; i < 30; i++ {
		// Target i shares 2 + i%6 hub words, so strengths are graded.
		shared := hubWords[:2+i%6]
		text := strings.Join(shared, " ") + fmt.Sprintf(" filler%d extra%d", i, i)
		id := fmt.Sprintf("t%02d", i)
		claims = append(claims, domain.Claim{ID: id, Text: text, Type: domain.ClaimTypeFact,
			Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: at.Add(time.Duration(i) * time.Minute)})
		rels = append(rels, domain.Relationship{ID: "s-" + id, Type: domain.RelationshipTypeSupports,
			FromClaimID: "hub", ToClaimID: id, CreatedAt: at})
	}
	rels = append(rels, domain.Relationship{ID: "c-t00", Type: domain.RelationshipTypeContradicts,
		FromClaimID: "hub", ToClaimID: "t00", CreatedAt: at})
	if err := conn.Claims.Upsert(ctx, claims); err != nil {
		t.Fatal(err)
	}
	if err := conn.Relationships.Upsert(ctx, rels); err != nil {
		t.Fatal(err)
	}
	countHub := func() (supports int, contradicts int, ids []string) {
		edges, err := conn.Relationships.ListByClaim(ctx, "hub")
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range edges {
			switch e.Type {
			case domain.RelationshipTypeSupports:
				supports++
				ids = append(ids, e.ToClaimID)
			case domain.RelationshipTypeContradicts:
				contradicts++
			}
		}
		sort.Strings(ids)
		return
	}

	dry, err := runPruneSupports(ctx, conn.Claims, conn.Relationships, pruneSupportsOpts{topK: 5}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dry.dropped != 25 || dry.deleted != 0 || dry.overBudget != 1 {
		t.Fatalf("dry run = %+v, want 25 to drop, 0 deleted, 1 claim over budget", dry)
	}
	if s, _, _ := countHub(); s != 30 {
		t.Fatalf("dry run deleted edges: %d supports left, want 30", s)
	}

	applied, err := runPruneSupports(ctx, conn.Claims, conn.Relationships, pruneSupportsOpts{topK: 5, apply: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if applied.deleted != 25 {
		t.Fatalf("apply deleted %d edges, want 25", applied.deleted)
	}
	s, c, kept := countHub()
	if s != 5 || c != 1 {
		t.Fatalf("after apply: %d supports, %d contradicts; want 5 and 1", s, c)
	}
	// The strongest five share all 7 hub words: i%6 == 5, i = 5,11,17,23,29.
	if want := []string{"t05", "t11", "t17", "t23", "t29"}; strings.Join(kept, ",") != strings.Join(want, ",") {
		t.Fatalf("kept %v, want the strongest %v", kept, want)
	}

	again, err := runPruneSupports(ctx, conn.Claims, conn.Relationships, pruneSupportsOpts{topK: 5, apply: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.dropped != 0 || again.deleted != 0 {
		t.Fatalf("second apply = %+v, want a no-op", again)
	}
}

func TestPruneSupports_ArgParsing(t *testing.T) {
	o, err := parsePruneSupportsArgs([]string{"--top-k", "7", "--apply"}, Flags{})
	if err != nil || o.topK != 7 || !o.apply {
		t.Fatalf("parse = %+v, %v", o, err)
	}
	if o, _ := parsePruneSupportsArgs([]string{"--apply"}, Flags{DryRun: true}); o.apply {
		t.Error("--dry-run must override --apply")
	}
	if o, _ := parsePruneSupportsArgs(nil, Flags{}); o.apply || o.topK != 20 {
		t.Errorf("defaults = %+v, want report-only with top-k 20", o)
	}
	for _, bad := range [][]string{{"--top-k"}, {"--top-k", "0"}, {"--top-k", "x"}, {"evt-1"}} {
		if _, err := parsePruneSupportsArgs(bad, Flags{}); err == nil {
			t.Errorf("parse %v: want an error", bad)
		}
	}
}

// The prune is a whole-brain pass measured at over an hour on a real brain;
// the default 10-minute job bound killed it halfway. It gets the maintenance
// bound unless the operator set one.
func TestPruneSupports_RunsUnderTheMaintenanceTimeout(t *testing.T) {
	t.Setenv("MNEMOS_JOB_TIMEOUT", "")
	if got := maintenanceJobTimeout(); got < time.Hour {
		t.Errorf("maintenance timeout = %s, want at least an hour", got)
	}
	t.Setenv("MNEMOS_JOB_TIMEOUT", "90s")
	if got := maintenanceJobTimeout(); got != 90*time.Second {
		t.Errorf("explicit MNEMOS_JOB_TIMEOUT ignored: got %s", got)
	}
}
