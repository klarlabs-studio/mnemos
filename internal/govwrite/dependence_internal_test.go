package govwrite

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// independence must answer the same for every episode whatever order they
// are asked in. ev_a depends on ev_b and on the retracted ev_r; ev_b depends
// back on ev_a, so both reach ev_r. Asking ev_a first used to memoise the
// cycle cut's "independent" for ev_b and then serve it as ev_b's answer.
func TestIndependence_CycleIsOrderIndependent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w, err := New(ctx, "memory://", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	now := time.Now().UTC()
	var events []domain.Event
	for _, id := range []string{"ev_r", "ev_a", "ev_b", "ev_c", "ev_d", "ev_e"} {
		events = append(events, domain.Event{ID: id, RunID: "r1", Content: "content of " + id, SchemaVersion: "1.0",
			SourceInputID: "src-" + id, Timestamp: now, IngestedAt: now, CreatedBy: "tester"})
	}
	if _, err := w.Events(ctx, events); err != nil {
		t.Fatalf("seed events: %v", err)
	}
	for _, d := range []domain.EventDependency{
		// Tainted cycle: a -> {b, r}, b -> a.
		{EventID: "ev_a", DependsOn: "ev_b", Kind: domain.DependencyCites},
		{EventID: "ev_a", DependsOn: "ev_r", Kind: domain.DependencyCopies},
		{EventID: "ev_b", DependsOn: "ev_a", Kind: domain.DependencyCites},
		// Clean cycle: c -> d -> c, and e -> c.
		{EventID: "ev_c", DependsOn: "ev_d", Kind: domain.DependencyCites},
		{EventID: "ev_d", DependsOn: "ev_c", Kind: domain.DependencyCites},
		{EventID: "ev_e", DependsOn: "ev_c", Kind: domain.DependencyCites},
	} {
		d.CreatedBy = "tester"
		if _, err := w.DeclareEventDependency(ctx, d); err != nil {
			t.Fatalf("declare %s -> %s: %v", d.EventID, d.DependsOn, err)
		}
	}
	want := map[string]bool{"ev_r": false, "ev_a": false, "ev_b": false, "ev_c": true, "ev_d": true, "ev_e": true}
	for _, order := range [][]string{
		{"ev_a", "ev_b", "ev_c", "ev_d", "ev_e", "ev_r"},
		{"ev_b", "ev_a", "ev_e", "ev_d", "ev_c", "ev_r"},
		{"ev_r", "ev_e", "ev_c", "ev_b", "ev_d", "ev_a"},
	} {
		x := newIndependence(w.Conn(), map[string]bool{"ev_r": true})
		for _, id := range order {
			got, err := x.counts(ctx, id)
			if err != nil {
				t.Fatalf("counts(%s): %v", id, err)
			}
			if got != want[id] {
				t.Errorf("order %v: counts(%s) = %v, want %v", order, id, got, want[id])
			}
		}
	}
}
