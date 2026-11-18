package retraction_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
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
