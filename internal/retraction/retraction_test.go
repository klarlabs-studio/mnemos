package retraction_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/retraction"

	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

func newWriter(t *testing.T) *govwrite.Writer {
	t.Helper()
	w, err := govwrite.New(context.Background(), "sqlite://"+filepath.Join(t.TempDir(), "r.db"), nil)
	if err != nil {
		t.Fatalf("govwrite.New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	now := time.Now().UTC()
	if _, err := w.Events(context.Background(), []domain.Event{{
		ID: "ev_1", RunID: "run-a", Content: "c", SchemaVersion: "1.0", SourceInputID: "s", Timestamp: now, IngestedAt: now, CreatedBy: "x",
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return w
}

func TestRun_ValidatesBeforeRetracting(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	called := 0
	spy := func(ctx context.Context, id string, note domain.ChangeNote) (domain.EventRetraction, error) {
		called++
		return w.RetractEvent(ctx, id, note)
	}
	cases := []struct {
		name string
		req  retraction.Request
		want error
	}{
		{"empty id", retraction.Request{EventID: "  "}, retraction.ErrEventIDRequired},
		{"unknown event", retraction.Request{EventID: "ev_missing"}, retraction.ErrEventNotFound},
		{"run outside scope reads as not found", retraction.Request{EventID: "ev_1", RunAllowed: func(r string) bool { return r == "run-b" }}, retraction.ErrEventNotFound},
	}
	for _, tc := range cases {
		if _, err := retraction.Run(ctx, w.Conn().Events, spy, tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if called != 0 {
		t.Fatalf("retraction ran %d time(s) for rejected requests", called)
	}

	rep, err := retraction.Run(ctx, w.Conn().Events, spy, retraction.Request{
		EventID: " ev_1 ", Reason: " stale ", Actor: "alice", RunAllowed: func(r string) bool { return r == "run-a" },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.EventID != "ev_1" || rep.Reason != "stale" || rep.RetractedBy != "alice" {
		t.Errorf("record = %+v, want trimmed id/reason and the actor", rep)
	}
	view := retraction.NewReport(rep)
	if view.EpisodeID != "ev_1" || view.RetractedAt == "" || view.Impact.RetractedBeliefs == nil {
		t.Errorf("report = %+v", view)
	}
}

func TestUnretract_ValidatesBeforeUnretracting(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	conn := w.Conn()
	called := 0
	spy := func(ctx context.Context, id string, note domain.ChangeNote) (domain.EventUnretraction, error) {
		called++
		return w.UnretractEvent(ctx, id, note)
	}
	cases := []struct {
		name string
		req  retraction.Request
		want error
	}{
		{"empty id", retraction.Request{EventID: "  "}, retraction.ErrEventIDRequired},
		{"unknown event", retraction.Request{EventID: "ev_missing"}, retraction.ErrEventNotFound},
		{"run outside scope reads as not found", retraction.Request{EventID: "ev_1", RunAllowed: func(r string) bool { return r == "run-b" }}, retraction.ErrEventNotFound},
		{"never retracted", retraction.Request{EventID: "ev_1"}, retraction.ErrNotRetracted},
	}
	for _, tc := range cases {
		if _, err := retraction.Unretract(ctx, conn.Events, conn.Journal, spy, tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if called != 0 {
		t.Fatalf("un-retraction ran %d time(s) for rejected requests", called)
	}

	if _, err := w.RetractEvent(ctx, "ev_1", domain.ChangeNote{ChangedBy: "bob"}); err != nil {
		t.Fatalf("retract: %v", err)
	}
	rep, err := retraction.Unretract(ctx, conn.Events, conn.Journal, spy, retraction.Request{
		EventID: " ev_1 ", Reason: " was fine ", Actor: "alice", RunAllowed: func(r string) bool { return r == "run-a" },
	})
	if err != nil {
		t.Fatalf("Unretract: %v", err)
	}
	if rep.EventID != "ev_1" || rep.Reason != "was fine" || rep.UnretractedBy != "alice" || rep.RetractedBy != "bob" {
		t.Errorf("record = %+v, want trimmed id/reason, the actor and the cleared retraction", rep)
	}
	view := retraction.NewUnretractionReport(rep)
	if view.EpisodeID != "ev_1" || view.UnretractedAt == "" || view.Generation != 1 || view.Impact.Restored == nil {
		t.Errorf("report = %+v", view)
	}

	// A repeat is not an error: the stored record comes back.
	again, err := retraction.Unretract(ctx, conn.Events, conn.Journal, spy, retraction.Request{EventID: "ev_1"})
	if err != nil || !again.AlreadyUnretracted {
		t.Errorf("repeat: %+v, %v; want the stored record with AlreadyUnretracted", again, err)
	}
}

// unbatchedJournal hides ports.JournalSubjectsLister, forcing LoadHistories
// onto its per-event fallback.
type unbatchedJournal struct{ ports.JournalRepository }

// LoadHistories agrees with LoadHistory for every event, batched or not:
// retracted, retracted then restored, and never retracted.
func TestLoadHistories_MatchesLoadHistory(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := w.Events(ctx, []domain.Event{
		{ID: "ev_2", RunID: "run-a", Content: "c2", SchemaVersion: "1.0", SourceInputID: "s2", Timestamp: now, IngestedAt: now, CreatedBy: "x"},
		{ID: "ev_3", RunID: "run-a", Content: "c3", SchemaVersion: "1.0", SourceInputID: "s3", Timestamp: now, IngestedAt: now, CreatedBy: "x"},
	}); err != nil {
		t.Fatal(err)
	}
	note := domain.ChangeNote{Reason: "r", ChangedBy: "x"}
	for _, id := range []string{"ev_1", "ev_2"} {
		if _, err := w.RetractEvent(ctx, id, note); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.UnretractEvent(ctx, "ev_2", note); err != nil {
		t.Fatal(err)
	}
	ids := []string{"ev_1", "ev_2", "ev_3"}
	journal := w.Conn().Journal
	if _, ok := journal.(ports.JournalSubjectsLister); !ok {
		t.Fatal("sqlite journal does not batch; the batched path is untested")
	}
	for name, j := range map[string]ports.JournalRepository{"batched": journal, "per-event": unbatchedJournal{journal}} {
		got, err := retraction.LoadHistories(ctx, j, ids)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, id := range ids {
			want, err := retraction.LoadHistory(ctx, journal, id)
			if err != nil {
				t.Fatal(err)
			}
			_, gotActive := got[id].Active()
			_, wantActive := want.Active()
			if gotActive != wantActive || len(got[id].Retractions) != len(want.Retractions) || len(got[id].Unretractions) != len(want.Unretractions) {
				t.Errorf("%s: %s = %+v, want %+v", name, id, got[id], want)
			}
		}
	}
}
