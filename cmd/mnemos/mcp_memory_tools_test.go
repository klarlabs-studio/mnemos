package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// setIsolatedDB points the process at a fresh sqlite DB under t.TempDir
// so each MCP-tool test gets an empty store. The mcpRun* funcs read
// MNEMOS_DB_URL through openConn → resolveDSN, so a per-test env switch
// is the simplest seam.
func setIsolatedDB(t *testing.T) {
	t.Helper()
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "mnemos.db"))
}

// TestMCP_Remember_CreatesClaimAndEventAndEvidence pins the happy
// path: a single Remember call produces a claim, an event, and an
// evidence link tying them, so the claim shows up under a run_id
// filter.
func TestMCP_Remember_CreatesClaimAndEventAndEvidence(t *testing.T) {
	setIsolatedDB(t)
	out, err := mcpRunRemember(context.Background(), "test-actor", mcpRememberInput{
		Text:  "compaction reduces SSTable count to ~3 in production",
		RunID: "tenant:A",
		Kind:  "fact",
	})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if out.ClaimID == "" || out.EventID == "" {
		t.Fatalf("Remember returned empty IDs: %+v", out)
	}
	if out.RunID != "tenant:A" || out.Status != "active" {
		t.Errorf("unexpected output: %+v", out)
	}

	// Verify the claim is visible to a run_id-scoped listing — the
	// load-bearing contract: Remember must wire the event link so
	// run_id-filtered recall (and #36 semantic search) can find it.
	conn, err := openConn(context.Background())
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	defer closeConn(conn)
	events, err := conn.Events.ListByRunID(context.Background(), "tenant:A")
	if err != nil {
		t.Fatalf("ListByRunID: %v", err)
	}
	if len(events) != 1 || events[0].ID != out.EventID {
		t.Errorf("event not linked under run_id: got %+v", events)
	}
	links, err := conn.Claims.ListEvidenceByClaimIDs(context.Background(), []string{out.ClaimID})
	if err != nil {
		t.Fatalf("ListEvidenceByClaimIDs: %v", err)
	}
	if len(links) != 1 || links[0].EventID != out.EventID {
		t.Errorf("evidence link missing: got %+v", links)
	}
}

// TestMCP_Remember_PropagatesValidUntil proves that the agent can
// flag a claim with a TTL — the gRPC bitemporal query in #35 reads
// the same field, so Remember has to be the write-side counterpart.
func TestMCP_Remember_PropagatesValidUntil(t *testing.T) {
	setIsolatedDB(t)
	validUntil := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	out, err := mcpRunRemember(context.Background(), "test-actor", mcpRememberInput{
		Text:       "rollback policy ABC ends tomorrow",
		RunID:      "tenant:A",
		ValidUntil: validUntil.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	conn, err := openConn(context.Background())
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	defer closeConn(conn)
	got, err := conn.Claims.ListByIDs(context.Background(), []string{out.ClaimID})
	if err != nil || len(got) != 1 {
		t.Fatalf("ListByIDs: %v / %+v", err, got)
	}
	if !got[0].ValidTo.Equal(validUntil) {
		t.Errorf("ValidTo = %v, want %v", got[0].ValidTo, validUntil)
	}
}

// TestMCP_Remember_ValidationFailures pins the fail-closed contract:
// missing text, missing run_id, malformed valid_until, bad confidence,
// or unsupported kind must error rather than silently default to
// something surprising.
func TestMCP_Remember_ValidationFailures(t *testing.T) {
	setIsolatedDB(t)
	cases := []struct {
		name  string
		input mcpRememberInput
	}{
		{"missing text", mcpRememberInput{RunID: "tenant:A"}},
		{"missing run_id", mcpRememberInput{Text: "x"}},
		{"bad kind", mcpRememberInput{Text: "x", RunID: "tenant:A", Kind: "test_result"}},
		{"bad valid_until", mcpRememberInput{Text: "x", RunID: "tenant:A", ValidUntil: "tomorrow"}},
		{"confidence too high", mcpRememberInput{Text: "x", RunID: "tenant:A", Confidence: 1.5}},
		{"confidence negative", mcpRememberInput{Text: "x", RunID: "tenant:A", Confidence: -0.1}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, err := mcpRunRemember(context.Background(), "a", tc.input); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// TestMCP_Forget_FlipsStatusToDeprecated pins the agent's "forget"
// path: status moves from active to deprecated without touching text
// or evidence, so audit history stays queryable.
func TestMCP_Forget_FlipsStatusToDeprecated(t *testing.T) {
	setIsolatedDB(t)
	remembered, err := mcpRunRemember(context.Background(), "a", mcpRememberInput{
		Text: "to-be-forgotten", RunID: "tenant:A",
	})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}

	out, err := mcpRunForget(context.Background(), "a", mcpForgetInput{
		ClaimID: remembered.ClaimID,
		Reason:  "no longer relevant",
	})
	if err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if out.OldStatus != "active" || out.NewStatus != "deprecated" {
		t.Errorf("unexpected transition: %+v", out)
	}

	conn, err := openConn(context.Background())
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	defer closeConn(conn)
	got, _ := conn.Claims.ListByIDs(context.Background(), []string{remembered.ClaimID})
	if got[0].Status != "deprecated" {
		t.Errorf("Status = %q, want deprecated", got[0].Status)
	}
	if got[0].Text != "to-be-forgotten" {
		t.Errorf("text mutated by forget: %q", got[0].Text)
	}
}

// TestMCP_Forget_UnknownClaim errors rather than silently no-op.
// "Forget a non-existent claim" almost certainly means a bug in the
// caller, so we surface it.
func TestMCP_Forget_UnknownClaim(t *testing.T) {
	setIsolatedDB(t)
	if _, err := mcpRunForget(context.Background(), "a", mcpForgetInput{ClaimID: "no_such_claim"}); err == nil {
		t.Fatal("expected error for unknown claim")
	}
}

// TestMCP_Update_RewritesText keeps the claim ID stable and only
// rewrites the text + (optionally) confidence; status remains active.
func TestMCP_Update_RewritesText(t *testing.T) {
	setIsolatedDB(t)
	remembered, err := mcpRunRemember(context.Background(), "a", mcpRememberInput{
		Text: "initial wording", RunID: "tenant:A",
	})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	out, err := mcpRunUpdate(context.Background(), "a", mcpUpdateInput{
		ClaimID:    remembered.ClaimID,
		NewText:    "refined wording",
		Confidence: 0.95,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.OldText != "initial wording" || out.NewText != "refined wording" {
		t.Errorf("Update I/O wrong: %+v", out)
	}
	conn, _ := openConn(context.Background())
	defer closeConn(conn)
	got, _ := conn.Claims.ListByIDs(context.Background(), []string{remembered.ClaimID})
	if got[0].Text != "refined wording" {
		t.Errorf("Text not persisted: %q", got[0].Text)
	}
	if got[0].Confidence != 0.95 {
		t.Errorf("Confidence = %v, want 0.95", got[0].Confidence)
	}
	if got[0].Status != "active" {
		t.Errorf("Status mutated by update: %q", got[0].Status)
	}
}

// TestMCP_Update_RejectsBadInput pins the validation surface — empty
// or malformed inputs error rather than smuggling junk into the
// store.
func TestMCP_Update_RejectsBadInput(t *testing.T) {
	setIsolatedDB(t)
	cases := []struct {
		name  string
		input mcpUpdateInput
	}{
		{"missing claim_id", mcpUpdateInput{NewText: "x"}},
		{"missing new_text", mcpUpdateInput{ClaimID: "any"}},
		{"confidence out of range", mcpUpdateInput{ClaimID: "any", NewText: "x", Confidence: 1.5}},
		{"unknown claim", mcpUpdateInput{ClaimID: "no_such", NewText: "x"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, err := mcpRunUpdate(context.Background(), "a", tc.input); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// TestMCP_SearchMemory_RanksAndScopesByRunID pins the semantic-recall
// happy path: matches in tenant A surface, tenant B claims do NOT
// even though they would score higher in absolute terms.
func TestMCP_SearchMemory_RanksAndScopesByRunID(t *testing.T) {
	setIsolatedDB(t)
	withStubEmbedder(t, &stubEmbedder{vector: []float32{1, 0, 0}})

	// Two claims in tenant A — different cosine alignment with the query.
	aPerfect, err := mcpRunRemember(context.Background(), "a", mcpRememberInput{
		Text: "A perfect", RunID: "tenant:A",
	})
	if err != nil {
		t.Fatalf("remember A perfect: %v", err)
	}
	aMid, err := mcpRunRemember(context.Background(), "a", mcpRememberInput{
		Text: "A mid", RunID: "tenant:A",
	})
	if err != nil {
		t.Fatalf("remember A mid: %v", err)
	}
	// Tenant B — same content, MUST NOT leak.
	bPerfect, err := mcpRunRemember(context.Background(), "a", mcpRememberInput{
		Text: "B perfect", RunID: "tenant:B",
	})
	if err != nil {
		t.Fatalf("remember B: %v", err)
	}

	// Backfill stored embeddings (Remember does not auto-embed in the
	// current pipeline).
	conn, _ := openConn(context.Background())
	defer closeConn(conn)
	for _, fixture := range []struct {
		id  string
		vec []float32
	}{
		{aPerfect.ClaimID, []float32{1, 0, 0}},
		{aMid.ClaimID, []float32{1, 1, 0}},
		{bPerfect.ClaimID, []float32{1, 0, 0}},
	} {
		if err := conn.Embeddings.Upsert(context.Background(), fixture.id, "claim", fixture.vec, "m", ""); err != nil {
			t.Fatalf("upsert embedding: %v", err)
		}
	}

	out, err := mcpRunSearchMemory(context.Background(), mcpSearchMemoryInput{
		Query: "anything", RunID: "tenant:A", TopK: 10,
	})
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(out.Hits) != 2 {
		t.Fatalf("got %d hits, want 2 (only tenant A)", len(out.Hits))
	}
	for _, h := range out.Hits {
		if h.ClaimID == bPerfect.ClaimID {
			t.Fatal("tenant B claim leaked into tenant A search")
		}
	}
	if out.Hits[0].ClaimID != aPerfect.ClaimID {
		t.Errorf("rank[0] = %q, want %q", out.Hits[0].ClaimID, aPerfect.ClaimID)
	}
	if out.Hits[0].Similarity < out.Hits[1].Similarity {
		t.Errorf("not sorted desc: %+v", out.Hits)
	}
}

// TestMCP_SearchMemory_RequiresRunID — fail-closed: no run_id, no
// search. Mirrors HTTP semantic search.
func TestMCP_SearchMemory_RequiresRunID(t *testing.T) {
	setIsolatedDB(t)
	withStubEmbedder(t, &stubEmbedder{vector: []float32{1, 0, 0}})
	if _, err := mcpRunSearchMemory(context.Background(), mcpSearchMemoryInput{Query: "q"}); err == nil {
		t.Fatal("expected error when run_id missing")
	}
}

// TestMCP_SearchMemory_NoEmbedderConfigured surfaces a clear error
// instead of silently returning [], so an agent never thinks an empty
// result means "no matches" when really the provider is unconfigured.
func TestMCP_SearchMemory_NoEmbedderConfigured(t *testing.T) {
	setIsolatedDB(t)
	// Seed a candidate so the path reaches the embedder.
	if _, err := mcpRunRemember(context.Background(), "a", mcpRememberInput{
		Text: "x", RunID: "tenant:A",
	}); err != nil {
		t.Fatalf("remember: %v", err)
	}
	withEmbedderError(t, errors.New("MNEMOS_EMBED_PROVIDER unset"))
	if _, err := mcpRunSearchMemory(context.Background(), mcpSearchMemoryInput{
		Query: "q", RunID: "tenant:A",
	}); err == nil {
		t.Fatal("expected error when embedder unconfigured")
	}
}
