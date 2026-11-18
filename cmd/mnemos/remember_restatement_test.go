package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/client"
	"go.klarlabs.de/mnemos/internal/domain"
)

// Restating a belief the brain already holds folds onto it on every surface
// that writes a single fact — REST POST /v1/facts, the Go client, MCP remember
// and MCP remember_belief — exactly as ingest does: the held belief's id comes
// back with known set, its evidence grows, and no claim row is added. gRPC
// RememberFact is covered in internal/server/grpc. Asserted on what the store
// holds, not on what the calls were handed.
func TestRememberFact_RestatementFoldsOnEveryTransport(t *testing.T) {
	f := newBeliefOpsFixture(t)
	ctx := context.Background()
	writer := f.token(t, domain.ScopeClaimsWrite)
	const text = "The billing service runs in eu-west-1."

	claimRows := func() int {
		t.Helper()
		all, err := f.conn.Claims.ListAll(ctx)
		if err != nil {
			t.Fatalf("list claims: %v", err)
		}
		return len(all)
	}
	evidenceOf := func(id string) int {
		t.Helper()
		links, err := f.conn.Claims.ListEvidenceByClaimIDs(ctx, []string{id})
		if err != nil {
			t.Fatalf("list evidence: %v", err)
		}
		return len(links)
	}

	var first factResponse
	if code := f.call(t, http.MethodPost, "/v1/facts", writer, map[string]any{"text": text, "evidence": []string{"runbook p.4"}}, &first); code != http.StatusCreated || first.Known {
		t.Fatalf("first POST /v1/facts = %d %+v, want 201 new", code, first)
	}
	held := first.BeliefID

	var again factResponse
	if code := f.call(t, http.MethodPost, "/v1/facts", writer, map[string]any{"text": "the billing service runs in EU-WEST-1", "evidence": []string{"incident 812"}}, &again); code != http.StatusOK || again.BeliefID != held || !again.Known {
		t.Fatalf("REST restatement = %d %+v, want 200 %s known", code, again, held)
	}
	if n := evidenceOf(held); n != 2 {
		t.Fatalf("after REST restatement: %d evidence links, want 2", n)
	}

	c := client.New(f.srv.URL, client.WithToken(writer))
	got, err := c.RememberClaimWithEvidenceDetailed(ctx, text, []string{"dashboard"}, time.Time{})
	if err != nil || got.ID != held || !got.Known {
		t.Fatalf("client restatement = %+v, %v; want %s known", got, err, held)
	}
	if id, err := c.RememberClaimWithEvidence(ctx, text, []string{"pager"}, time.Time{}); err != nil || id != held {
		t.Fatalf("client RememberClaimWithEvidence = %q, %v; want %s", id, err, held)
	}

	mcpOut, err := mcpRunRemember(ctx, "tester", mcpRememberInput{Text: text, RunID: "run-a"})
	if err != nil || mcpOut.ClaimID != held || !mcpOut.Known {
		t.Fatalf("MCP remember restatement = %+v, %v; want %s known", mcpOut, err, held)
	}
	if n := evidenceOf(held); n != 5 {
		t.Fatalf("after client and MCP restatements: %d evidence links, want 5", n)
	}

	mem, err := newLibraryMemory(ctx, "")
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	seedEventConn(t, f.conn, "ev-ops", "run-a", "ops review notes", "src", "{}", time.Now().UTC())
	belief, err := mcpRunRememberBelief(ctx, mem, "agent-7", mcpRememberBeliefInput{Text: text, EpisodeIDs: []string{"ev-ops"}})
	if err != nil || belief.BeliefID != held || !belief.Known {
		t.Fatalf("MCP remember_belief restatement = %+v, %v; want %s known", belief, err, held)
	}
	if n := evidenceOf(held); n != 6 {
		t.Fatalf("after remember_belief: %d evidence links, want 6", n)
	}
	if n := claimRows(); n != 1 {
		t.Fatalf("store holds %d claims after restatements on every transport, want 1", n)
	}

	var fresh factResponse
	if code := f.call(t, http.MethodPost, "/v1/facts", writer, map[string]any{"text": "Payments owns the billing service.", "evidence": []string{"org chart"}}, &fresh); code != http.StatusCreated || fresh.Known || fresh.BeliefID == held {
		t.Fatalf("new fact = %d %+v, want 201 and a new belief", code, fresh)
	}
	if n := claimRows(); n != 2 {
		t.Fatalf("store holds %d claims, want 2", n)
	}
}
