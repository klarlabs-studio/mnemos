package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	mnemos "go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// mcpBeliefOpsBrain pins a throwaway SQLite brain and returns the Memory the
// MCP server would hand a tool, plus a direct conn to read results back.
func mcpBeliefOpsBrain(t *testing.T) (mnemos.Memory, *store.Conn) {
	t.Helper()
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "mcp-ops.db")
	t.Setenv("MNEMOS_DB_URL", dsn)
	mem, err := newLibraryMemory(context.Background(), "")
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	conn, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return mem, conn
}

func mcpRunScopedCtx() context.Context {
	return withClaims(context.Background(), &auth.Claims{Scopes: []string{"*"}, Runs: []string{"run-a"}})
}

func TestMCP_ListEpisodesAndRememberBelief(t *testing.T) {
	mem, conn := mcpBeliefOpsBrain(t)
	ctx := context.Background()
	seeded, err := mcpRunRemember(ctx, "tester", mcpRememberInput{Text: "the queue drains hourly", RunID: "run-a"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	eps, err := mcpRunListEpisodes(ctx, mcpListEpisodesInput{RunID: "run-a"})
	if err != nil || eps.Total != 1 || eps.Episodes[0].ID != seeded.EventID {
		t.Fatalf("list_episodes = %+v, %v", eps, err)
	}
	if other, _ := mcpRunListEpisodes(ctx, mcpListEpisodesInput{RunID: "run-b"}); other.Total != 0 {
		t.Errorf("list_episodes(run-b) = %+v, want none", other)
	}
	if _, err := mcpRunListEpisodes(mcpRunScopedCtx(), mcpListEpisodesInput{}); err == nil {
		t.Error("list_episodes without a run accepted a run-scoped token")
	}

	if _, err := mcpRunRememberBelief(ctx, mem, "tester", mcpRememberBeliefInput{Text: "x"}); err == nil {
		t.Error("remember_belief accepted a belief without evidence")
	}
	out, err := mcpRunRememberBelief(ctx, mem, "agent-7", mcpRememberBeliefInput{
		Text: "queue drains are hourly on weekdays", Type: "fact", Confidence: 0.7,
		EpisodeIDs: []string{seeded.EventID}, ValidFrom: "2026-02-01",
	})
	if err != nil || out.BeliefID == "" {
		t.Fatalf("remember_belief = %+v, %v", out, err)
	}
	got, err := conn.Claims.ListByIDs(ctx, []string{out.BeliefID})
	if err != nil || len(got) != 1 || got[0].Text != "queue drains are hourly on weekdays" || got[0].CreatedBy != "agent-7" {
		t.Fatalf("stored belief = %+v, %v", got, err)
	}
	links, err := conn.Claims.ListEvidenceByClaimIDs(ctx, []string{out.BeliefID})
	if err != nil || len(links) != 1 || links[0].EventID != seeded.EventID {
		t.Errorf("stored evidence = %+v, %v", links, err)
	}

	scan, err := mcpRunScanBeliefs(ctx, mem, mcpScanBeliefsInput{ValidFrom: "2026-01-15", ValidUntil: "2026-02-15"})
	if err != nil || len(scan.Beliefs) != 1 || scan.Beliefs[0].ID != out.BeliefID {
		t.Errorf("scan_beliefs = %+v, %v", scan, err)
	}
	if _, err := mcpRunScanBeliefs(mcpRunScopedCtx(), mem, mcpScanBeliefsInput{}); err == nil {
		t.Error("scan_beliefs accepted a run-scoped token")
	}
}

func TestMCP_LifecycleProvenanceHistory(t *testing.T) {
	mem, conn := mcpBeliefOpsBrain(t)
	ctx := context.Background()
	seeded, err := mcpRunRemember(ctx, "tester", mcpRememberInput{Text: "billing runs in eu-west-1", RunID: "run-a"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	memFor := func(context.Context, claimBrain) (mnemos.Memory, error) { return mem, nil }

	if _, err := mcpRunSetBeliefLifecycle(ctx, memFor, mcpSetBeliefLifecycleInput{ClaimID: seeded.ClaimID, Lifecycle: "bogus"}); err == nil {
		t.Error("set_belief_lifecycle accepted an unknown lifecycle")
	}
	if _, err := mcpRunSetBeliefLifecycle(ctx, memFor, mcpSetBeliefLifecycleInput{ClaimID: seeded.ClaimID, Lifecycle: "promoted"}); err != nil {
		t.Fatalf("set_belief_lifecycle: %v", err)
	}
	got, err := conn.Claims.ListByIDs(ctx, []string{seeded.ClaimID})
	if err != nil || len(got) != 1 || got[0].Lifecycle != domain.ClaimLifecycle("promoted") {
		t.Errorf("stored lifecycle = %+v, %v", got, err)
	}

	prov, err := mcpRunBeliefProvenance(ctx, mcpBeliefIDInput{ClaimID: seeded.ClaimID})
	if err != nil || prov.ClaimID != seeded.ClaimID || prov.ClaimText != "billing runs in eu-west-1" || len(prov.Signals) == 0 {
		t.Errorf("belief_provenance = %+v, %v", prov, err)
	}
	if _, err := mcpRunUpdate(ctx, "tester", mcpUpdateInput{ClaimID: seeded.ClaimID, NewText: "billing runs in eu-central-1"}); err != nil {
		t.Fatal(err)
	}
	hist, err := mcpRunBeliefHistory(ctx, mcpBeliefIDInput{ClaimID: seeded.ClaimID})
	if err != nil || len(hist.Versions) == 0 || hist.Versions[0].Text != "billing runs in eu-central-1" {
		t.Errorf("belief_history = %+v, %v", hist, err)
	}
}

func TestMCP_ListAssociationsAndEntityAssociations(t *testing.T) {
	_, conn := mcpBeliefOpsBrain(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedClaimConn(t, conn, "cl-1", "a", "fact", "active", 0.9, now)
	seedClaimConn(t, conn, "cl-2", "b", "fact", "active", 0.9, now)
	seedRelationshipConn(t, conn, "rel-s", "supports", "cl-1", "cl-2", now)
	seedRelationshipConn(t, conn, "rel-c", "contradicts", "cl-2", "cl-1", now)
	if err := conn.EntityRels.Upsert(ctx, []domain.EntityRelationship{{
		ID: "er-1", Kind: domain.RelationshipTypeActionOf, FromID: "ac-1", FromType: "action",
		ToID: "oc-1", ToType: "outcome", CreatedAt: now, CreatedBy: "seed",
	}}); err != nil {
		t.Fatal(err)
	}

	all, err := mcpRunListAssociations(ctx, mcpListAssociationsInput{})
	if err != nil || all.Total != 2 {
		t.Errorf("list_associations = %+v, %v", all, err)
	}
	sup, err := mcpRunListAssociations(ctx, mcpListAssociationsInput{Type: "supports"})
	if err != nil || sup.Total != 1 || sup.Associations[0].ID != "rel-s" {
		t.Errorf("list_associations(supports) = %+v, %v", sup, err)
	}
	if _, err := mcpRunListAssociations(ctx, mcpListAssociationsInput{Type: "causes"}); err == nil {
		t.Error("list_associations accepted an unknown type")
	}
	if _, err := mcpRunListAssociations(mcpRunScopedCtx(), mcpListAssociationsInput{}); err == nil {
		t.Error("list_associations accepted a run-scoped token")
	}

	edges, err := mcpRunListEntityAssociations(ctx, mcpListEntityAssociationsInput{EntityID: "ac-1", EntityType: "action"})
	if err != nil || edges.Total != 1 || edges.Edges[0].ToID != "oc-1" {
		t.Errorf("list_entity_associations = %+v, %v", edges, err)
	}
	if _, err := mcpRunListEntityAssociations(ctx, mcpListEntityAssociationsInput{EntityID: "ac-1"}); err == nil {
		t.Error("list_entity_associations accepted entity_id without entity_type")
	}
}
