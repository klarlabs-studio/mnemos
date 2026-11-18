package mnemos

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/pipeline"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// A restatement of a belief the brain already holds must fold onto it on the
// pre-built-claim write path exactly as it does on ingest: the existing belief
// gains the new evidence, the caller gets the existing id back, and no second
// claim row is written. scalebench found RememberClaim writing a fresh row for
// every restatement (20 of 20), each with its own tokens and edges.

// foldBackends are the stores the fold is checked against. Postgres and MySQL
// run the same lookups (ListLiveByTextKeys) and are covered by the
// integration suite.
func foldBackends(t *testing.T) map[string]string {
	return map[string]string{
		"memory": "memory://?namespace=fold_restate",
		"sqlite": "sqlite://" + filepath.Join(t.TempDir(), "fold.db"),
	}
}

func foldMem(t *testing.T, dsn string) *memory {
	t.Helper()
	for _, k := range []string{"MNEMOS_DB_URL", "MNEMOS_STORAGE", "MNEMOS_MODE", "MNEMOS_LLM_PROVIDER", "MNEMOS_LLM_API_KEY", "MNEMOS_EMBED_PROVIDER", "MNEMOS_USER_ID"} {
		t.Setenv(k, "")
	}
	mem, err := New(WithStorage(dsn), WithPassiveMode())
	if err != nil {
		t.Fatalf("New(%s): %v", dsn, err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	return mem.(*memory)
}

// storedClaims returns every claim row the store holds.
func storedClaims(t *testing.T, m *memory) []domain.Claim {
	t.Helper()
	cs, err := m.conn.Claims.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list claims: %v", err)
	}
	return cs
}

// storedEvidence returns the evidence links the store holds for one claim.
func storedEvidence(t *testing.T, m *memory, claimID string) []domain.ClaimEvidence {
	t.Helper()
	ev, err := m.conn.Claims.ListEvidenceByClaimIDs(context.Background(), []string{claimID})
	if err != nil {
		t.Fatalf("list evidence: %v", err)
	}
	return ev
}

func TestRememberClaimWithEvidence_RestatementFoldsOntoHeldBelief(t *testing.T) {
	for name, dsn := range foldBackends(t) {
		t.Run(name, func(t *testing.T) {
			m := foldMem(t, dsn)
			ctx := context.Background()
			const text = "The billing service runs in eu-west-1."

			first, err := m.RememberClaimWithEvidenceDetailed(ctx, text, []string{"runbook p.4"}, time.Time{})
			if err != nil {
				t.Fatalf("first write: %v", err)
			}
			if first.Known {
				t.Fatal("first write reported the belief as already known")
			}

			again, err := m.RememberClaimWithEvidenceDetailed(ctx, "the billing service runs in EU-WEST-1", []string{"incident 812"}, time.Time{})
			if err != nil {
				t.Fatalf("restatement: %v", err)
			}
			if again.ID != first.ID || !again.Known {
				t.Fatalf("restatement = %+v, want id %s known", again, first.ID)
			}
			if got := len(storedClaims(t, m)); got != 1 {
				t.Fatalf("store holds %d claims after a restatement, want 1", got)
			}
			if got := len(storedEvidence(t, m, first.ID)); got != 2 {
				t.Fatalf("held belief has %d evidence links, want 2 (the restatement's evidence attached)", got)
			}

			// The legacy signature returns the canonical id too.
			id, err := m.RememberClaimWithEvidence(ctx, text, []string{"dashboard"}, time.Time{})
			if err != nil || id != first.ID {
				t.Fatalf("RememberClaimWithEvidence = %q, %v; want %s", id, err, first.ID)
			}

			// Repeating evidence already held is not corroboration (#443).
			if _, err := m.RememberClaimWithEvidenceDetailed(ctx, text, []string{"runbook p.4"}, time.Time{}); err != nil {
				t.Fatalf("repeat: %v", err)
			}
			if got := len(storedEvidence(t, m, first.ID)); got != 3 {
				t.Fatalf("repeated evidence changed the link count to %d, want 3", got)
			}

			fresh, err := m.RememberClaimWithEvidenceDetailed(ctx, "The billing service is owned by the payments team.", []string{"org chart"}, time.Time{})
			if err != nil {
				t.Fatalf("new fact: %v", err)
			}
			if fresh.Known || fresh.ID == first.ID {
				t.Fatalf("new fact = %+v, want a new belief", fresh)
			}
			if got := len(storedClaims(t, m)); got != 2 {
				t.Fatalf("store holds %d claims, want 2", got)
			}
		})
	}
}

func TestRememberClaim_RestatementWithStoredEventsFolds(t *testing.T) {
	m := foldMem(t, "memory://?namespace=fold_stored_events")
	ctx := context.Background()
	for _, ev := range []Event{{ID: "ev-a", At: time.Now().UTC(), Content: "deploy log a"}, {ID: "ev-b", At: time.Now().UTC(), Content: "deploy log b"}} {
		if err := m.RememberEvent(ctx, ev); err != nil {
			t.Fatalf("RememberEvent: %v", err)
		}
	}
	first, err := m.RememberClaim(ctx, ClaimItem{Text: "Deploys run at 09:00 UTC.", EventIDs: []string{"ev-a"}})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	again, err := m.RememberClaimDetailed(ctx, ClaimItem{Text: "Deploys run at 09:00 UTC.", EventIDs: []string{"ev-b"}})
	if err != nil {
		t.Fatalf("restatement: %v", err)
	}
	if again.ID != first || !again.Known {
		t.Fatalf("restatement = %+v, want id %s known", again, first)
	}
	if got := len(storedClaims(t, m)); got != 1 {
		t.Fatalf("store holds %d claims, want 1", got)
	}
	if got := len(storedEvidence(t, m, first)); got != 2 {
		t.Fatalf("held belief has %d evidence links, want 2", got)
	}

	// A claim stated as already ended is history, not a restatement of the
	// live belief: it is stored as its own row.
	ended, err := m.RememberClaimDetailed(ctx, ClaimItem{Text: "Deploys run at 09:00 UTC.", EventIDs: []string{"ev-b"}, ValidUntil: time.Now().UTC()})
	if err != nil {
		t.Fatalf("ended: %v", err)
	}
	if ended.Known || ended.ID == first {
		t.Fatalf("ended claim = %+v, want its own row", ended)
	}
}

// Remember takes the session from Item.Metadata["session_id"], the key every
// session-aware reader uses, and stamps it on the episode under that key.
func TestRemember_SessionMetadataRoundTrips(t *testing.T) {
	m := foldMem(t, "memory://?namespace=fold_session")
	ctx := context.Background()
	const content = "The staging cluster autoscales between 3 and 12 nodes."
	if err := m.Remember(ctx, Item{Content: content, Metadata: map[string]string{"session_id": "sess-1"}}); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	events, err := m.conn.Events.ListAll(ctx)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("stored %d events, want 1", len(events))
	}
	if got := events[0].Metadata[pipeline.SessionMetadataKey]; got != "sess-1" {
		t.Fatalf("episode session = %q, want sess-1 (metadata %v)", got, events[0].Metadata)
	}
	if _, stale := events[0].Metadata["input_session_id"]; stale {
		t.Fatalf("episode still carries the prefixed key: %v", events[0].Metadata)
	}

	claims := storedClaims(t, m)
	if len(claims) == 0 {
		t.Fatal("Remember stored no claim")
	}
	held := claims[0].ID
	before := len(storedEvidence(t, m, held))

	// Same session again: one origin, so no new evidence and no new episode.
	if err := m.Remember(ctx, Item{Content: content, Metadata: map[string]string{"session_id": "sess-1"}}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got := len(storedEvidence(t, m, held)); got != before {
		t.Fatalf("replaying the session changed evidence %d -> %d", before, got)
	}
	if evs, _ := m.conn.Events.ListAll(ctx); len(evs) != 1 {
		t.Fatalf("replaying the session stored %d events, want 1", len(evs))
	}
	// A different session is an independent origin.
	if err := m.Remember(ctx, Item{Content: content, Metadata: map[string]string{"session_id": "sess-2"}}); err != nil {
		t.Fatalf("second session: %v", err)
	}
	if got := len(storedEvidence(t, m, held)); got != before+1 {
		t.Fatalf("a second session gave %d evidence links, want %d", got, before+1)
	}
	if got := len(storedClaims(t, m)); got != len(claims) {
		t.Fatalf("restating in a new session stored %d claims, want %d", got, len(claims))
	}
}
