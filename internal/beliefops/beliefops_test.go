package beliefops_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/beliefops"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/memory"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// fixture is one backend and one way of writing to it. Every test runs on
// each backend with both the governed daemon writer and the library's direct
// writer, because the operations promise the same result on all of them.
type fixture struct {
	name string
	conn *store.Conn
	w    beliefops.Writer
	ew   beliefops.EntityWriter
}

func fixtures(t *testing.T) []fixture {
	t.Helper()
	var out []fixture
	for _, backend := range []string{"memory", "sqlite"} {
		for _, mode := range []string{"governed", "direct"} {
			dsn := "memory://"
			if backend == "sqlite" {
				dsn = "sqlite://" + filepath.Join(t.TempDir(), "brain.db")
			}
			conn, err := store.Open(context.Background(), dsn)
			if err != nil {
				t.Fatalf("open %s: %v", dsn, err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			gw, err := govwrite.Wrap(conn, nil)
			if err != nil {
				t.Fatalf("wrap: %v", err)
			}
			f := fixture{name: backend + "/" + mode, conn: conn, ew: gw}
			if mode == "governed" {
				f.w = gw
			} else {
				f.w = govwrite.Direct(conn)
			}
			out = append(out, f)
		}
	}
	return out
}

func seed(t *testing.T, conn *store.Conn, id, text string) {
	t.Helper()
	now := time.Now().UTC()
	if err := conn.Claims.Upsert(context.Background(), []domain.Claim{{
		ID: id, Text: text, Type: domain.ClaimTypeFact, Confidence: 0.8,
		Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now, CreatedBy: "seed",
		ConfidenceComponents: map[string]float64{"corroboration": 0.5},
	}}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func stored(t *testing.T, conn *store.Conn, id string) domain.Claim {
	t.Helper()
	got, err := conn.Claims.ListByIDs(context.Background(), []string{id})
	if err != nil || len(got) != 1 {
		t.Fatalf("read %s back: %v (%d rows)", id, err, len(got))
	}
	return got[0]
}

func TestGovernanceEdits_PersistOnEveryBackendAndWriter(t *testing.T) {
	ctx := context.Background()
	for _, f := range fixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			seed(t, f.conn, "a", "the cache is warm")
			seed(t, f.conn, "b", "the cache is cold")
			seed(t, f.conn, "c", "deploys run on tuesdays")

			ch, err := beliefops.Deprecate(ctx, f.conn, f.w, beliefops.Edit{ClaimID: "c", Actor: "alice"}, beliefops.ReasonForget)
			if err != nil || ch.OldStatus != "active" || ch.NewStatus != "deprecated" {
				t.Fatalf("Deprecate = %+v, %v", ch, err)
			}
			if got := stored(t, f.conn, "c").Status; got != domain.ClaimStatusDeprecated {
				t.Errorf("stored status after deprecate = %s", got)
			}

			rev, err := beliefops.Update(ctx, f.conn, f.w, beliefops.UpdateRequest{
				Edit: beliefops.Edit{ClaimID: "a", Actor: "alice"}, Text: "the cache is warm after 9am", Confidence: 0.6,
			})
			if err != nil || rev.OldText != "the cache is warm" {
				t.Fatalf("Update = %+v, %v", rev, err)
			}
			if got := stored(t, f.conn, "a"); got.Text != "the cache is warm after 9am" || got.Confidence != 0.6 {
				t.Errorf("stored after update = %q %.2f", got.Text, got.Confidence)
			}

			if _, err := beliefops.Resolve(ctx, f.conn, f.w, beliefops.ResolveRequest{WinnerID: "a", LoserID: "b"}); err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if a, b := stored(t, f.conn, "a").Status, stored(t, f.conn, "b").Status; a != domain.ClaimStatusResolved || b != domain.ClaimStatusDeprecated {
				t.Errorf("stored after resolve: winner %s loser %s", a, b)
			}

			at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
			if _, err := beliefops.Verify(ctx, f.conn, f.w, "a", at); err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if got := stored(t, f.conn, "a"); !got.LastVerified.Equal(at) || got.VerifyCount != 1 {
				t.Errorf("stored after verify: last_verified %v count %d", got.LastVerified, got.VerifyCount)
			}

			esc, err := beliefops.Escalate(ctx, f.conn, "a", "needs a human")
			if err != nil || esc.EscalationReason != "needs a human" || esc.Action != string(domain.VerdictActionEscalate) {
				t.Errorf("Escalate = %+v, %v", esc, err)
			}
		})
	}
}

func TestGovernanceEdits_RejectUnknownAndMalformed(t *testing.T) {
	ctx := context.Background()
	f := fixtures(t)[0]
	seed(t, f.conn, "a", "x")
	cases := map[string]struct {
		err  error
		want error
	}{
		"deprecate unknown": {err: second(beliefops.Deprecate(ctx, f.conn, f.w, beliefops.Edit{ClaimID: "nope"}, "")), want: beliefops.ErrNotFound},
		"update empty text": {err: second(beliefops.Update(ctx, f.conn, f.w, beliefops.UpdateRequest{Edit: beliefops.Edit{ClaimID: "a"}})), want: beliefops.ErrInvalid},
		"resolve self":      {err: second(beliefops.Resolve(ctx, f.conn, f.w, beliefops.ResolveRequest{WinnerID: "a", LoserID: "a"})), want: beliefops.ErrInvalid},
		"resolve missing":   {err: second(beliefops.Resolve(ctx, f.conn, f.w, beliefops.ResolveRequest{WinnerID: "a", LoserID: "nope"})), want: beliefops.ErrNotFound},
		"verify unknown":    {err: second(beliefops.Verify(ctx, f.conn, f.w, "nope", time.Now())), want: beliefops.ErrNotFound},
		"escalate unknown":  {err: second(beliefops.Escalate(ctx, f.conn, "nope", "")), want: beliefops.ErrNotFound},
		"provenance gone":   {err: second(beliefops.Provenance(ctx, f.conn, "nope")), want: beliefops.ErrNotFound},
		"feedback unknown": {err: second(beliefops.RecordFeedback(ctx, f.conn, f.w, beliefops.FeedbackRequest{ClaimID: "nope"},
			beliefops.FeedbackPolicy{Decay: 0.9, ContestThreshold: 3}, time.Now())), want: beliefops.ErrNotFound},
	}
	for name, tc := range cases {
		if !errors.Is(tc.err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, tc.err, tc.want)
		}
	}
}

func second[T any](_ T, err error) error { return err }

func TestRecordFeedback_DecaysAndContestsOnEveryBackendAndWriter(t *testing.T) {
	ctx := context.Background()
	policy := beliefops.FeedbackPolicy{Decay: 0.5, ContestThreshold: 2}
	for _, f := range fixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			seed(t, f.conn, "a", "the cache is warm")
			first, err := beliefops.RecordFeedback(ctx, f.conn, f.w, beliefops.FeedbackRequest{ClaimID: "a", Note: " meh "}, policy, time.Now())
			if err != nil || first.AutoContested || first.NegativeFeedbackStreak != 1 {
				t.Fatalf("first vote = %+v, %v", first, err)
			}
			second, err := beliefops.RecordFeedback(ctx, f.conn, f.w, beliefops.FeedbackRequest{ClaimID: "a"}, policy, time.Now())
			if err != nil || !second.AutoContested {
				t.Fatalf("second vote = %+v, %v", second, err)
			}
			got := stored(t, f.conn, "a")
			if got.Status != domain.ClaimStatusContested || got.Confidence != 0.2 {
				t.Errorf("stored after two unhelpful votes: %s %.3f, want contested 0.200", got.Status, got.Confidence)
			}
			state, ok, err := f.conn.Feedback.Get(ctx, "a")
			if err != nil || !ok || state.NegativeFeedbackStreak != 2 {
				t.Errorf("stored feedback = %+v ok=%v err=%v", state, ok, err)
			}
			if _, err := beliefops.RecordFeedback(ctx, f.conn, f.w, beliefops.FeedbackRequest{ClaimID: "a", Helpful: true}, policy, time.Now()); err != nil {
				t.Fatal(err)
			}
			state, _, _ = f.conn.Feedback.Get(ctx, "a")
			if state.NegativeFeedbackStreak != 0 || state.HelpfulCount != 1 {
				t.Errorf("helpful vote: stored streak %d helpful %d", state.NegativeFeedbackStreak, state.HelpfulCount)
			}
		})
	}
}

func TestEntityRelationships_AppendThenListOnEveryBackend(t *testing.T) {
	ctx := context.Background()
	for _, f := range fixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			n, err := beliefops.AppendEntityRelationships(ctx, f.ew, []domain.EntityRelationship{
				{ID: "er1", Kind: domain.RelationshipTypeActionOf, FromID: "ac1", FromType: "action", ToID: "oc1", ToType: "outcome"},
			}, "alice", time.Now())
			if err != nil || n != 1 {
				t.Fatalf("append = %d, %v", n, err)
			}
			if _, err := beliefops.AppendEntityRelationships(ctx, f.ew, []domain.EntityRelationship{{ID: "bad"}}, "alice", time.Now()); !errors.Is(err, beliefops.ErrInvalid) {
				t.Errorf("invalid edge: err = %v", err)
			}
			for _, filter := range []beliefops.EntityFilter{{}, {Kind: "action_of"}, {EntityID: "ac1", EntityType: "action"}} {
				edges, err := beliefops.ListEntityRelationships(ctx, f.conn, filter)
				if err != nil || len(edges) != 1 || edges[0].CreatedBy != "alice" || edges[0].ToID != "oc1" {
					t.Errorf("list %+v = %+v, %v", filter, edges, err)
				}
			}
			if _, err := beliefops.ListEntityRelationships(ctx, f.conn, beliefops.EntityFilter{EntityID: "ac1"}); !errors.Is(err, beliefops.ErrInvalid) {
				t.Errorf("entity id without type: err = %v", err)
			}
		})
	}
}

func TestProvenanceAndHistory_ReadWhatTheStoreHolds(t *testing.T) {
	ctx := context.Background()
	for _, f := range fixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			seed(t, f.conn, "a", "the cache is warm")
			rep, err := beliefops.Provenance(ctx, f.conn, "a")
			if err != nil || rep.ClaimID != "a" || rep.ClaimText != "the cache is warm" {
				t.Errorf("Provenance = %+v, %v", rep, err)
			}
			if _, err := beliefops.Update(ctx, f.conn, f.w, beliefops.UpdateRequest{Edit: beliefops.Edit{ClaimID: "a"}, Text: "the cache is cold"}); err != nil {
				t.Fatal(err)
			}
			versions, err := beliefops.History(ctx, f.conn, "a")
			if errors.Is(err, beliefops.ErrUnsupported) {
				t.Skip("backend keeps no version history")
			}
			if err != nil || len(versions) == 0 || versions[0].Text != "the cache is cold" {
				t.Errorf("History = %+v, %v", versions, err)
			}
		})
	}
}
