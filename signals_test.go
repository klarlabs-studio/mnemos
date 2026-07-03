package mnemos_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// seedActivity writes a rising cadence of same-type events under runID, enough
// for the bundled Chronos engine to detect a temporal pattern.
func seedActivity(t *testing.T, mem mnemos.Memory, runID string) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().Add(-48 * time.Hour)
	n := 0
	for day := 0; day < 14; day++ {
		for i := 0; i <= day; i++ {
			n++
			ev := mnemos.Event{
				ID:      fmt.Sprintf("e-%s-%d", runID, n),
				At:      base.Add(time.Duration(day)*time.Hour + time.Duration(i)*time.Minute),
				Type:    "incident",
				Content: "incident occurred",
				RunID:   runID,
			}
			if err := mem.RememberEvent(ctx, ev); err != nil {
				t.Fatalf("RememberEvent: %v", err)
			}
		}
	}
}

func newSignalMemory(t *testing.T, ns string) mnemos.Memory {
	t.Helper()
	clearMnemosEnv(t)
	mem, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace="+ns),
		mnemos.WithPassiveMode(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	return mem
}

// TestSignals_DetectsAndShapes verifies Signals surfaces a detected temporal
// pattern with a valid public shape and the queried run id.
func TestSignals_DetectsAndShapes(t *testing.T) {
	mem := newSignalMemory(t, "signals_detect")
	ctx := context.Background()
	seedActivity(t, mem, "run-a")

	sigs, err := mem.Signals(ctx, mnemos.SignalQuery{RunID: "run-a"})
	if err != nil {
		t.Fatalf("Signals: %v", err)
	}
	if len(sigs) == 0 {
		t.Fatal("expected at least one signal from the seeded activity, got none")
	}
	for _, s := range sigs {
		if s.Pattern == "" {
			t.Error("signal has empty Pattern")
		}
		if s.RunID != "run-a" {
			t.Errorf("signal RunID = %q, want run-a", s.RunID)
		}
		if s.Confidence < 0 || s.Confidence > 1 {
			t.Errorf("signal Confidence = %v, want [0,1]", s.Confidence)
		}
	}
}

// TestSignals_ScopeIsolation verifies signals are read from the exact run scope
// events were written under: a different run sees nothing.
func TestSignals_ScopeIsolation(t *testing.T) {
	mem := newSignalMemory(t, "signals_scope")
	ctx := context.Background()
	seedActivity(t, mem, "run-a")

	other, err := mem.Signals(ctx, mnemos.SignalQuery{RunID: "run-b"})
	if err != nil {
		t.Fatalf("Signals(run-b): %v", err)
	}
	if len(other) != 0 {
		t.Errorf("run-b (no events) returned %d signals; scope keying is wrong", len(other))
	}
}

// TestSignals_MinConfidenceFilters verifies the confidence floor drops signals
// below it (a threshold above 1 excludes everything).
func TestSignals_MinConfidenceFilters(t *testing.T) {
	mem := newSignalMemory(t, "signals_minconf")
	ctx := context.Background()
	seedActivity(t, mem, "run-a")

	none, err := mem.Signals(ctx, mnemos.SignalQuery{RunID: "run-a", MinConfidence: 1.01})
	if err != nil {
		t.Fatalf("Signals: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("MinConfidence 1.01 returned %d signals; want 0 (nothing exceeds 1.0)", len(none))
	}
}
