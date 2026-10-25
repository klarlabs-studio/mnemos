package pipeline

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"
)

// knownClaimStores opens every backend the write path runs on in tests: the
// in-memory store and SQLite. The SQL backends answer the same lookup, which
// internal/store's parity suite holds them to.
func knownClaimStores(t *testing.T) map[string]*store.Conn {
	t.Helper()
	ctx := context.Background()
	out := map[string]*store.Conn{}
	for name, dsn := range map[string]string{
		"memory": "memory://",
		"sqlite": "sqlite://" + filepath.Join(t.TempDir(), "known.db"),
	} {
		conn, err := store.Open(ctx, dsn)
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		out[name] = conn
	}
	return out
}

var knownAt = time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)

// captureEvent is one captured chunk of a session, the shape capture writes.
func captureEvent(id, session, content string) domain.Event {
	return domain.Event{
		ID: id, RunID: "run-" + id, SchemaVersion: "v1", Content: content,
		SourceInputID: "in-" + id, Timestamp: knownAt, IngestedAt: knownAt,
		Metadata: map[string]string{SessionMetadataKey: session}, CreatedBy: "claude-code",
	}
}

func knownClaim(id, text string) domain.Claim {
	return domain.Claim{
		ID: id, Text: text, Type: domain.ClaimTypeFact, Confidence: 0.8,
		Status: domain.ClaimStatusActive, CreatedAt: knownAt, CreatedBy: "claude-code",
	}
}

// persistOne writes one event and the claims it evidences, the way a capture
// chunk reaches the store.
func persistOne(t *testing.T, conn *store.Conn, ev domain.Event, claims ...domain.Claim) {
	t.Helper()
	links := make([]domain.ClaimEvidence, len(claims))
	for i, c := range claims {
		links[i] = domain.ClaimEvidence{ClaimID: c.ID, EventID: ev.ID}
	}
	if err := PersistArtifacts(context.Background(), conn, []domain.Event{ev}, claims, links, nil); err != nil {
		t.Fatalf("persist %s: %v", ev.ID, err)
	}
}

func evidenceEvents(t *testing.T, conn *store.Conn, claimID string) []string {
	t.Helper()
	links, err := conn.Claims.ListEvidenceByClaimIDs(context.Background(), []string{claimID})
	if err != nil {
		t.Fatalf("list evidence: %v", err)
	}
	ids := make([]string, len(links))
	for i, l := range links {
		ids[i] = l.EventID
	}
	sort.Strings(ids)
	return ids
}

func claimCount(t *testing.T, conn *store.Conn) int64 {
	t.Helper()
	n, err := conn.Claims.CountAll(context.Background())
	if err != nil {
		t.Fatalf("count claims: %v", err)
	}
	return n
}

// A belief restated in a later session is the same belief. It must gain the
// new session's evidence, not a second row — the 62.8% of a real brain that
// was exact-duplicate text.
func TestPersistArtifacts_RepeatedBeliefLinksEvidenceToTheExistingClaim(t *testing.T) {
	for name, conn := range knownClaimStores(t) {
		persistOne(t, conn, captureEvent("ev-s1", "session-1", "The retry budget is 3 attempts."),
			knownClaim("cl-first", "The retry budget is 3 attempts"))
		persistOne(t, conn, captureEvent("ev-s2", "session-2", "the retry budget is 3 attempts!"),
			knownClaim("cl-again", "the retry budget is 3 attempts."))

		if n := claimCount(t, conn); n != 1 {
			t.Errorf("%s: %d claims after restating one belief, want 1", name, n)
		}
		if got := evidenceEvents(t, conn, "cl-first"); len(got) != 2 {
			t.Errorf("%s: evidence of the held claim = %v, want both sessions", name, got)
		}
	}
}

// Re-ingesting the same session — the Sep 19–20 replay that added 56k claims —
// must change nothing a reader can see: no row, and no evidence that would let
// repetition raise trust (#443, #390). The replayed chunk is cut differently,
// so its event is not the original one.
func TestPersistArtifacts_ReplayOfTheSameSessionAddsNoClaimAndNoEvidence(t *testing.T) {
	for name, conn := range knownClaimStores(t) {
		persistOne(t, conn, captureEvent("ev-orig", "session-1", "The retry budget is 3 attempts."),
			knownClaim("cl-held", "The retry budget is 3 attempts"))
		before := trustEvidenceCount(t, conn, "cl-held")

		// Twice: under the current repeat weight (0.5, floored) a single repeat
		// would not move the corroboration count, and the test would pass
		// against the bug it exists to catch.
		for _, id := range []string{"ev-replay-1", "ev-replay-2"} {
			persistOne(t, conn, captureEvent(id, "session-1", "Recap "+id+". The retry budget is 3 attempts."),
				knownClaim("cl-"+id, "The retry budget is 3 attempts"))
		}

		if n := claimCount(t, conn); n != 1 {
			t.Errorf("%s: %d claims after a replay, want 1", name, n)
		}
		if got := evidenceEvents(t, conn, "cl-held"); len(got) != 1 || got[0] != "ev-orig" {
			t.Errorf("%s: replay attached evidence %v, want only ev-orig", name, got)
		}
		if after := trustEvidenceCount(t, conn, "cl-held"); after != before {
			t.Errorf("%s: replay moved the corroboration input %d -> %d", name, before, after)
		}
	}
}

// The same document ingested again outside any session is the same origin by
// content (#390's "one document ingested 500 times").
func TestPersistArtifacts_IdenticalContentAgainIsTheSameOrigin(t *testing.T) {
	for name, conn := range knownClaimStores(t) {
		doc := "The cache is warmed on deploy."
		first, again := captureEvent("ev-doc-1", "", doc), captureEvent("ev-doc-2", "", doc)
		first.Metadata, again.Metadata = nil, nil
		persistOne(t, conn, first, knownClaim("cl-doc-1", "The cache is warmed on deploy"))
		persistOne(t, conn, again, knownClaim("cl-doc-2", "The cache is warmed on deploy"))

		if n := claimCount(t, conn); n != 1 {
			t.Errorf("%s: %d claims after re-ingesting one document, want 1", name, n)
		}
		if got := evidenceEvents(t, conn, "cl-doc-1"); len(got) != 1 {
			t.Errorf("%s: re-ingesting identical content added evidence: %v", name, got)
		}
	}
}

// Only a LIVE claim in the same scope and audience holds a belief. A
// deprecated one, or one scoped elsewhere, must not swallow the new claim.
func TestPersistArtifacts_OnlyALiveClaimInTheSameScopeHoldsTheBelief(t *testing.T) {
	for name, conn := range knownClaimStores(t) {
		ctx := context.Background()
		dead := knownClaim("cl-dead", "The queue drains every minute")
		dead.Status = domain.ClaimStatusDeprecated
		scoped := knownClaim("cl-prod", "The queue drains every hour")
		scoped.Scope = domain.Scope{Env: "prod"}
		persistOne(t, conn, captureEvent("ev-old", "session-1", "old"), dead, scoped)

		persistOne(t, conn, captureEvent("ev-new", "session-2", "new"),
			knownClaim("cl-revived", "The queue drains every minute"),
			knownClaim("cl-unscoped", "The queue drains every hour"))

		got, err := conn.Claims.ListByIDs(ctx, []string{"cl-revived", "cl-unscoped"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Errorf("%s: folded onto a dead or differently scoped claim; stored %d of 2", name, len(got))
		}
	}
}

// Edges the batch drew to its own duplicate follow the claim that holds the
// belief; an edge from the belief to itself is dropped.
func TestPersistArtifacts_RelationshipsFollowTheHeldClaim(t *testing.T) {
	for name, conn := range knownClaimStores(t) {
		ctx := context.Background()
		persistOne(t, conn, captureEvent("ev-a", "session-1", "a"), knownClaim("cl-held-a", "Deploys run through kiln"))

		ev := captureEvent("ev-b", "session-2", "b")
		dup, other := knownClaim("cl-dup-a", "Deploys run through kiln"), knownClaim("cl-b", "Kiln signs every image")
		rels := []domain.Relationship{
			{ID: "rel-1", Type: domain.RelationshipTypeSupports, FromClaimID: dup.ID, ToClaimID: other.ID, CreatedAt: knownAt},
			{ID: "rel-self", Type: domain.RelationshipTypeSupports, FromClaimID: dup.ID, ToClaimID: "cl-held-a", CreatedAt: knownAt},
		}
		links := []domain.ClaimEvidence{{ClaimID: dup.ID, EventID: ev.ID}, {ClaimID: other.ID, EventID: ev.ID}}
		if err := PersistArtifacts(ctx, conn, []domain.Event{ev}, []domain.Claim{dup, other}, links, rels); err != nil {
			t.Fatalf("%s: persist: %v", name, err)
		}

		edges, err := conn.Relationships.ListByClaim(ctx, "cl-held-a")
		if err != nil {
			t.Fatal(err)
		}
		if len(edges) != 1 || edges[0].FromClaimID != "cl-held-a" || edges[0].ToClaimID != "cl-b" {
			t.Errorf("%s: edges of the held claim = %+v, want one cl-held-a -> cl-b", name, edges)
		}
	}
}

// Rewriting a claim under its own id is an update, not a duplicate of itself.
func TestPersistArtifacts_UpsertUnderTheSameIDIsNotADuplicate(t *testing.T) {
	for name, conn := range knownClaimStores(t) {
		persistOne(t, conn, captureEvent("ev-1", "session-1", "x"), knownClaim("cl-self", "Backups run nightly"))
		update := knownClaim("cl-self", "Backups run nightly")
		update.Confidence = 0.9
		persistOne(t, conn, captureEvent("ev-2", "session-2", "y"), update)

		got, err := conn.Claims.ListByIDs(context.Background(), []string{"cl-self"})
		if err != nil || len(got) != 1 || got[0].Confidence != 0.9 {
			t.Errorf("%s: update under the same id = %+v, %v; want confidence 0.9", name, got, err)
		}
	}
}

// CollapseKnownClaims reports which new claim folded onto which held one, so a
// caller that embeds or tags entities after persisting addresses the claim
// that exists.
func TestCollapseKnownClaims_ReportsTheRemap(t *testing.T) {
	conn := knownClaimStores(t)["memory"]
	persistOne(t, conn, captureEvent("ev-1", "session-1", "x"), knownClaim("cl-held", "Recall gathers beliefs first"))

	ev := captureEvent("ev-2", "session-2", "y")
	fresh := []domain.Claim{knownClaim("cl-new", "Recall gathers beliefs first"), knownClaim("cl-novel", "Recall is budgeted")}
	links := []domain.ClaimEvidence{{ClaimID: "cl-new", EventID: ev.ID}, {ClaimID: "cl-novel", EventID: ev.ID}}
	got, err := CollapseKnownClaims(context.Background(), conn, []domain.Event{ev}, fresh, links)
	if err != nil {
		t.Fatal(err)
	}
	if got.Remap["cl-new"] != "cl-held" || len(got.Claims) != 1 || got.Claims[0].ID != "cl-novel" {
		t.Errorf("collapse = %+v, want cl-new -> cl-held and only cl-novel left to write", got)
	}
}

// Entity tags extracted for a folded claim belong to the claim that holds it;
// left on the folded id they would tag a claim that is never stored.
func TestRemapEntities_MovesTagsToTheHeldClaim(t *testing.T) {
	tags := map[string][]extract.ExtractedEntity{
		"cl-new":   {{Name: "kiln", Type: "tool"}},
		"cl-held":  {{Name: "rollops", Type: "tool"}},
		"cl-other": {{Name: "warden", Type: "tool"}},
	}
	got := RemapEntities(tags, map[string]string{"cl-new": "cl-held"})
	if len(got["cl-held"]) != 2 || len(got["cl-other"]) != 1 || got["cl-new"] != nil {
		t.Errorf("remapped entities = %+v, want kiln and rollops on cl-held", got)
	}
}

func trustEvidenceCount(t *testing.T, conn *store.Conn, claimID string) int {
	t.Helper()
	lister, ok := conn.Claims.(ports.TrustInputLister)
	if !ok {
		return len(evidenceEvents(t, conn, claimID))
	}
	in, err := lister.ListTrustInputs(context.Background(), []string{claimID})
	if err != nil {
		t.Fatalf("list trust inputs: %v", err)
	}
	return in[claimID].EvidenceCount
}

// A belief merged away by semantic dedupe is a redirect (#387). Restating its
// text must land on the claim that absorbed it, through any chain of merges,
// and never on a target that is no longer live.
func TestPersistArtifacts_RestatingAMergedBeliefLandsOnItsWinner(t *testing.T) {
	for name, conn := range knownClaimStores(t) {
		ctx := context.Background()
		winner := knownClaim("cl-winner", "Deploys go out from the release branch")
		middle := knownClaim("cl-middle", "We deploy from the release branch")
		middle.Status, middle.MergedInto = domain.ClaimStatusDeprecated, winner.ID
		first := knownClaim("cl-first", "Deployments ship from the release branch")
		first.Status, first.MergedInto = domain.ClaimStatusDeprecated, middle.ID
		orphan := knownClaim("cl-orphan", "The cache is flushed nightly")
		orphan.Status, orphan.MergedInto = domain.ClaimStatusDeprecated, "cl-retired"
		retired := knownClaim("cl-retired", "Caches flush every night")
		retired.Status = domain.ClaimStatusDeprecated
		persistOne(t, conn, captureEvent("ev-old", "session-1", "old"), winner, middle, first, orphan, retired)

		persistOne(t, conn, captureEvent("ev-new", "session-2", "new"),
			knownClaim("cl-restated", "deployments ship from the release branch."),
			knownClaim("cl-relearned", "The cache is flushed nightly"))

		got, err := conn.Claims.ListByIDs(ctx, []string{"cl-restated", "cl-relearned"})
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, c := range got {
			ids[c.ID] = true
		}
		if ids["cl-restated"] {
			t.Errorf("%s: a restated merged belief became a new claim instead of following the redirect", name)
		}
		if ev := evidenceEvents(t, conn, winner.ID); !containsAll(ev, "ev-old", "ev-new") {
			t.Errorf("%s: winner evidence = %v, want the restatement's event through the merge chain", name, ev)
		}
		if !ids["cl-relearned"] {
			t.Errorf("%s: a redirect to a retired claim absorbed the restatement; it must come back as a new claim", name)
		}
	}
}
