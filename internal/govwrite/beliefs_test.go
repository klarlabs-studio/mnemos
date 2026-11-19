package govwrite_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/memory"
	_ "go.klarlabs.de/mnemos/internal/store/mysql"
	_ "go.klarlabs.de/mnemos/internal/store/postgres"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// ADR 0041: id-addressed writes (REST POST /v1/beliefs, gRPC AppendBeliefs,
// registry push and pull) follow merge redirects, and a new id restating a
// held belief becomes a redirect to it. Asserted on what each backend returns.

type namedWriter struct {
	name string
	w    *govwrite.Writer
}

// beliefBackends opens a governed writer per backend: memory and SQLite
// always, Postgres and MySQL when TEST_POSTGRES_DSN / TEST_MYSQL_DSN are set.
// Each live-server backend gets its own namespace so runs cannot collide.
func beliefBackends(t *testing.T) []namedWriter {
	t.Helper()
	ns := fmt.Sprintf("mnemos_test_beliefs_%d", time.Now().UnixNano())
	dsns := []struct{ name, dsn, drop string }{
		{name: "memory", dsn: "memory://"},
		{name: "sqlite", dsn: "sqlite://" + filepath.Join(t.TempDir(), "brain.db")},
	}
	if pg := os.Getenv("TEST_POSTGRES_DSN"); pg != "" {
		dsns = append(dsns, struct{ name, dsn, drop string }{"postgres", withNamespace(pg, ns), "DROP SCHEMA IF EXISTS " + ns + " CASCADE"})
	}
	if my := os.Getenv("TEST_MYSQL_DSN"); my != "" {
		dsns = append(dsns, struct{ name, dsn, drop string }{"mysql", withNamespace(my, ns), "DROP DATABASE IF EXISTS `" + ns + "`"})
	}
	out := make([]namedWriter, 0, len(dsns))
	for _, d := range dsns {
		w, err := govwrite.New(context.Background(), d.dsn, nil)
		if err != nil {
			t.Fatalf("open %s: %v", d.name, err)
		}
		t.Cleanup(func() {
			if db, ok := w.Conn().Raw.(*sql.DB); ok && d.drop != "" {
				_, _ = db.ExecContext(context.Background(), d.drop)
			}
			_ = w.Close()
		})
		out = append(out, namedWriter{name: d.name, w: w})
	}
	return out
}

func withNamespace(dsn, ns string) string {
	if strings.Contains(dsn, "?") {
		return dsn + "&namespace=" + ns
	}
	return dsn + "?namespace=" + ns
}

var beliefsAt = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

func liveClaim(id, text string) domain.Claim {
	return domain.Claim{
		ID: id, Text: text, Type: domain.ClaimTypeFact, Confidence: 0.8,
		Status: domain.ClaimStatusActive, CreatedAt: beliefsAt,
	}
}

func sessionEvent(id, session string) domain.Event {
	return domain.Event{
		ID: id, RunID: "run-" + session, SchemaVersion: "v1", Content: "said in " + session,
		SourceInputID: "in-" + id, Timestamp: beliefsAt, IngestedAt: beliefsAt,
		Metadata: map[string]string{"session_id": session},
	}
}

func readClaim(t *testing.T, conn *store.Conn, id string) domain.Claim {
	t.Helper()
	got, err := conn.Claims.ListByIDs(context.Background(), []string{id})
	if err != nil || len(got) != 1 {
		t.Fatalf("read claim %s: %v (%d rows)", id, err, len(got))
	}
	return got[0]
}

func evidenceOf(t *testing.T, conn *store.Conn, id string) []string {
	t.Helper()
	links, err := conn.Claims.ListEvidenceByClaimIDs(context.Background(), []string{id})
	if err != nil {
		t.Fatalf("evidence of %s: %v", id, err)
	}
	out := make([]string, 0, len(links))
	for _, l := range links {
		out = append(out, l.EventID)
	}
	sort.Strings(out)
	return out
}

func liveWithText(t *testing.T, conn *store.Conn, text string) []string {
	t.Helper()
	all, err := conn.Claims.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list claims: %v", err)
	}
	var out []string
	for _, c := range all {
		if c.Text == text && c.Status != domain.ClaimStatusDeprecated {
			out = append(out, c.ID)
		}
	}
	return out
}

func assertRedirect(t *testing.T, backend string, conn *store.Conn, id, to string) {
	t.Helper()
	got := readClaim(t, conn, id)
	if got.Status != domain.ClaimStatusDeprecated || got.MergedInto != to {
		t.Fatalf("%s: %s is status=%s merged_into=%q, want a deprecated redirect to %s",
			backend, id, got.Status, got.MergedInto, to)
	}
}

const sharedBelief = "the billing service retries failed charges three times"

// The verified failure: a registry consolidation merges A into W, and the next
// push of A — which the sending brain still holds active under its own id —
// came back ACTIVE with merged_into still set, and its evidence attached to the
// retired row instead of the belief.
func TestBeliefs_RedirectIsNotRevivedByAnIDAddressedWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, b := range beliefBackends(t) {
		conn := b.w.Conn()
		if _, err := b.w.Events(ctx, []domain.Event{sessionEvent("ev-w", "s-w"), sessionEvent("ev-a", "s-a"), sessionEvent("ev-a2", "s-a2")}); err != nil {
			t.Fatalf("%s: events: %v", b.name, err)
		}
		other := liveClaim("c-other", "invoices are generated nightly")
		if _, err := b.w.Beliefs(ctx,
			[]domain.Claim{liveClaim("c-w", sharedBelief), liveClaim("c-a", "billing retries failed charges three times"), other},
			[]domain.ClaimEvidence{{ClaimID: "c-w", EventID: "ev-w"}, {ClaimID: "c-a", EventID: "ev-a"}},
			govwrite.ClaimReason{Reason: "test seed"}); err != nil {
			t.Fatalf("%s: seed: %v", b.name, err)
		}
		if _, err := pipeline.ApplySemanticDedupe(ctx, conn, pipeline.SemanticDedupePlan{
			Merges: []pipeline.SemanticMerge{{WinnerID: "c-w", DuplicateIDs: []string{"c-a"}}},
		}); err != nil {
			t.Fatalf("%s: merge: %v", b.name, err)
		}
		assertRedirect(t, b.name, conn, "c-a", "c-w")

		// The sender re-pushes A as it holds it: active, with new evidence.
		res, err := b.w.Beliefs(ctx,
			[]domain.Claim{liveClaim("c-a", "billing retries failed charges three times")},
			[]domain.ClaimEvidence{{ClaimID: "c-a", EventID: "ev-a2"}},
			govwrite.ClaimReason{Reason: "api: POST /v1/beliefs", ChangedBy: "pusher"})
		if err != nil {
			t.Fatalf("%s: re-push: %v", b.name, err)
		}
		assertRedirect(t, b.name, conn, "c-a", "c-w")
		if res.Remapped["c-a"] != "c-w" {
			t.Fatalf("%s: remap = %v, want c-a -> c-w", b.name, res.Remapped)
		}
		if got := evidenceOf(t, conn, "c-a"); len(got) != 0 {
			t.Fatalf("%s: evidence attached to the redirect: %v", b.name, got)
		}
		if got, want := evidenceOf(t, conn, "c-w"), []string{"ev-a", "ev-a2", "ev-w"}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: winner evidence = %v, want %v", b.name, got, want)
		}

		// An association and an embedding addressed to the redirect follow it.
		if _, err := b.w.Relationships(ctx, []domain.Relationship{
			{ID: "r-a-other", Type: domain.RelationshipTypeSupports, FromClaimID: "c-a", ToClaimID: other.ID, CreatedAt: beliefsAt},
			{ID: "r-a-w", Type: domain.RelationshipTypeSupports, FromClaimID: "c-a", ToClaimID: "c-w", CreatedAt: beliefsAt},
		}); err != nil {
			t.Fatalf("%s: relationships: %v", b.name, err)
		}
		rels, err := conn.Relationships.ListByClaimIDs(ctx, []string{"c-a", "c-w", other.ID})
		if err != nil {
			t.Fatalf("%s: list relationships: %v", b.name, err)
		}
		if len(rels) != 1 || rels[0].FromClaimID != "c-w" || rels[0].ToClaimID != other.ID {
			t.Fatalf("%s: relationships = %+v, want only c-w -> %s", b.name, rels, other.ID)
		}
		if err := b.w.Embedding(ctx, "c-a", "claim", []float32{1, 0, 0}, "m", "pusher"); err != nil {
			t.Fatalf("%s: embedding: %v", b.name, err)
		}
		if n := claimEmbeddings(t, conn, "c-a"); n != 0 {
			t.Fatalf("%s: an embedding was stored for the redirect", b.name)
		}
	}
}

func claimEmbeddings(t *testing.T, conn *store.Conn, id string) int {
	t.Helper()
	all, err := conn.Embeddings.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list embeddings: %v", err)
	}
	n := 0
	for _, e := range all {
		if e.EntityType == "claim" && e.EntityID == id {
			n++
		}
	}
	return n
}

// Two brains mint random ids, so the same belief pushed from both used to land
// as two beliefs on the registry. The second id now becomes a redirect to the
// first, its evidence and association follow, its embedding is dropped, and
// pushing both again changes nothing.
func TestBeliefs_SameBeliefFromTwoBrainsIsOneBelief(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	type brain struct {
		claims    []domain.Claim
		links     []domain.ClaimEvidence
		events    []domain.Event
		rels      []domain.Relationship
		embedding string
	}
	brainA := brain{
		events:    []domain.Event{sessionEvent("ev-brain-a", "session-a")},
		claims:    []domain.Claim{liveClaim("a-belief", sharedBelief), liveClaim("a-other", "refunds take five days")},
		links:     []domain.ClaimEvidence{{ClaimID: "a-belief", EventID: "ev-brain-a"}, {ClaimID: "a-other", EventID: "ev-brain-a"}},
		rels:      []domain.Relationship{{ID: "a-rel", Type: domain.RelationshipTypeSupports, FromClaimID: "a-belief", ToClaimID: "a-other", CreatedAt: beliefsAt}},
		embedding: "a-belief",
	}
	brainB := brain{
		events:    []domain.Event{sessionEvent("ev-brain-b", "session-b")},
		claims:    []domain.Claim{liveClaim("b-belief", "  The billing service retries failed charges three times. "), liveClaim("b-other", "dunning emails go out weekly")},
		links:     []domain.ClaimEvidence{{ClaimID: "b-belief", EventID: "ev-brain-b"}, {ClaimID: "b-other", EventID: "ev-brain-b"}},
		rels:      []domain.Relationship{{ID: "b-rel", Type: domain.RelationshipTypeSupports, FromClaimID: "b-belief", ToClaimID: "b-other", CreatedAt: beliefsAt}},
		embedding: "b-belief",
	}
	push := func(t *testing.T, w *govwrite.Writer, br brain) govwrite.BeliefsResult {
		t.Helper()
		if _, err := w.Events(ctx, br.events); err != nil {
			t.Fatalf("events: %v", err)
		}
		res, err := w.Beliefs(ctx, br.claims, br.links, govwrite.ClaimReason{Reason: "api: POST /v1/beliefs", ChangedBy: "registry"})
		if err != nil {
			t.Fatalf("beliefs: %v", err)
		}
		if _, err := w.Relationships(ctx, br.rels); err != nil {
			t.Fatalf("relationships: %v", err)
		}
		if err := w.Embedding(ctx, br.embedding, "claim", []float32{0, 1, 0}, "m", "registry"); err != nil {
			t.Fatalf("embedding: %v", err)
		}
		return res
	}

	for _, b := range beliefBackends(t) {
		conn := b.w.Conn()
		for round := 1; round <= 2; round++ {
			resA := push(t, b.w, brainA)
			resB := push(t, b.w, brainB)
			if resA.Folded != 0 || len(resA.Remapped) != 0 {
				t.Fatalf("%s round %d: brain A was folded: %+v", b.name, round, resA)
			}
			if resB.Remapped["b-belief"] != "a-belief" {
				t.Fatalf("%s round %d: brain B remap = %v, want b-belief -> a-belief", b.name, round, resB.Remapped)
			}
			if want := map[int]int{1: 1, 2: 0}[round]; resB.Folded != want {
				t.Fatalf("%s round %d: folded = %d, want %d", b.name, round, resB.Folded, want)
			}

			if live := liveWithText(t, conn, sharedBelief); len(live) != 1 || live[0] != "a-belief" {
				t.Fatalf("%s round %d: live claims for the belief = %v, want [a-belief]", b.name, round, live)
			}
			assertRedirect(t, b.name, conn, "b-belief", "a-belief")
			if got := strings.Join(evidenceOf(t, conn, "a-belief"), ","); got != "ev-brain-a,ev-brain-b" {
				t.Fatalf("%s round %d: evidence = %s, want both brains'", b.name, round, got)
			}
			if got := evidenceOf(t, conn, "b-belief"); len(got) != 0 {
				t.Fatalf("%s round %d: redirect holds evidence %v", b.name, round, got)
			}
			rels, err := conn.Relationships.ListByClaimIDs(ctx, []string{"a-belief", "b-belief"})
			if err != nil {
				t.Fatalf("%s: relationships: %v", b.name, err)
			}
			ends := map[string]bool{}
			for _, r := range rels {
				if r.FromClaimID == "b-belief" || r.ToClaimID == "b-belief" {
					t.Fatalf("%s round %d: an association still addresses the redirect: %+v", b.name, round, r)
				}
				ends[r.FromClaimID+"->"+r.ToClaimID] = true
			}
			if !ends["a-belief->a-other"] || !ends["a-belief->b-other"] || len(rels) != 2 {
				t.Fatalf("%s round %d: associations = %+v, want a-belief -> a-other and a-belief -> b-other", b.name, round, rels)
			}
			if n := claimEmbeddings(t, conn, "b-belief"); n != 0 {
				t.Fatalf("%s round %d: orphan embedding stored for the redirect", b.name, round)
			}
			if n := claimEmbeddings(t, conn, "a-belief"); n != 1 {
				t.Fatalf("%s round %d: live belief has %d embeddings, want 1", b.name, round, n)
			}
		}
		h, err := conn.Claims.ListStatusHistoryByClaimID(ctx, "b-belief")
		if err != nil || len(h) == 0 {
			t.Fatalf("%s: history of the redirect: %v (%d rows)", b.name, err, len(h))
		}
		if !strings.Contains(h[len(h)-1].Reason, "restatement folded onto a-belief") {
			t.Fatalf("%s: redirect history reason = %q", b.name, h[len(h)-1].Reason)
		}
	}
}

// Identity includes scope and audience: the same words about another service,
// or for another audience, are another belief.
func TestBeliefs_DifferentScopeOrAudienceIsNotFolded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, b := range beliefBackends(t) {
		held := liveClaim("held", sharedBelief)
		held.Scope = domain.Scope{Service: "billing"}
		scoped := liveClaim("other-scope", sharedBelief)
		scoped.Scope = domain.Scope{Service: "payments"}
		audience := liveClaim("other-audience", sharedBelief)
		audience.Scope = domain.Scope{Service: "billing"}
		audience.Visibility = domain.VisibilityOrg
		if _, err := b.w.Beliefs(ctx, []domain.Claim{held}, nil, govwrite.ClaimReason{}); err != nil {
			t.Fatalf("%s: seed: %v", b.name, err)
		}
		res, err := b.w.Beliefs(ctx, []domain.Claim{scoped, audience}, nil, govwrite.ClaimReason{})
		if err != nil {
			t.Fatalf("%s: write: %v", b.name, err)
		}
		if res.Folded != 0 || len(res.Remapped) != 0 {
			t.Fatalf("%s: folded across scope or audience: %+v", b.name, res)
		}
		for _, id := range []string{"other-scope", "other-audience"} {
			if c := readClaim(t, b.w.Conn(), id); c.Status != domain.ClaimStatusActive || c.MergedInto != "" {
				t.Fatalf("%s: %s = %s/%q, want an active claim of its own", b.name, id, c.Status, c.MergedInto)
			}
		}
	}
}

// An explicit write to a stored live id is an edit of that claim, even when
// the new text restates another held belief; and a retired incoming claim is
// never folded onto a live one.
func TestBeliefs_KnownLiveIDStaysAnUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, b := range beliefBackends(t) {
		conn := b.w.Conn()
		if _, err := b.w.Beliefs(ctx, []domain.Claim{liveClaim("held", sharedBelief), liveClaim("known", "refunds take five days")}, nil, govwrite.ClaimReason{}); err != nil {
			t.Fatalf("%s: seed: %v", b.name, err)
		}
		edited := liveClaim("known", sharedBelief)
		edited.Confidence = 0.95
		retired := liveClaim("retired-new", sharedBelief)
		retired.Status = domain.ClaimStatusDeprecated
		res, err := b.w.Beliefs(ctx, []domain.Claim{edited, retired}, nil, govwrite.ClaimReason{})
		if err != nil {
			t.Fatalf("%s: write: %v", b.name, err)
		}
		if len(res.Remapped) != 0 {
			t.Fatalf("%s: remap = %v, want none", b.name, res.Remapped)
		}
		got := readClaim(t, conn, "known")
		if got.Text != sharedBelief || got.Confidence != 0.95 || got.Status != domain.ClaimStatusActive || got.MergedInto != "" {
			t.Fatalf("%s: known id was not updated in place: %+v", b.name, got)
		}
		if r := readClaim(t, conn, "retired-new"); r.MergedInto != "" || r.Status != domain.ClaimStatusDeprecated {
			t.Fatalf("%s: retired incoming claim = %s/%q", b.name, r.Status, r.MergedInto)
		}
	}
}

// A redirect whose chain ends at a claim that is no longer live is still not
// rewritten: its winner was retired on purpose, and reviving the merged copy
// would undo that by the back door. What is addressed to it stays on it.
func TestBeliefs_RedirectToARetiredWinnerStaysRetired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, b := range beliefBackends(t) {
		conn := b.w.Conn()
		if _, err := b.w.Events(ctx, []domain.Event{sessionEvent("ev-x", "s-x")}); err != nil {
			t.Fatalf("%s: events: %v", b.name, err)
		}
		winner := liveClaim("gone-winner", sharedBelief)
		winner.Status = domain.ClaimStatusDeprecated
		loser := liveClaim("gone-loser", sharedBelief)
		loser.Status = domain.ClaimStatusDeprecated
		loser.MergedInto = "gone-winner"
		if err := conn.Claims.Upsert(ctx, []domain.Claim{winner, loser}); err != nil {
			t.Fatalf("%s: seed: %v", b.name, err)
		}
		res, err := b.w.Beliefs(ctx, []domain.Claim{liveClaim("gone-loser", sharedBelief)},
			[]domain.ClaimEvidence{{ClaimID: "gone-loser", EventID: "ev-x"}}, govwrite.ClaimReason{})
		if err != nil {
			t.Fatalf("%s: write: %v", b.name, err)
		}
		if len(res.Remapped) != 0 {
			t.Fatalf("%s: remap = %v, want none", b.name, res.Remapped)
		}
		assertRedirect(t, b.name, conn, "gone-loser", "gone-winner")
		if got := evidenceOf(t, conn, "gone-loser"); len(got) != 1 {
			t.Fatalf("%s: evidence on the redirect = %v, want the link kept", b.name, got)
		}
	}
}
