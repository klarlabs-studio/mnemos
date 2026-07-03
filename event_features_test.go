package mnemos_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// seedRisingFeature writes a series of same-type events under runID whose numeric
// Feature RISES over time (day*10). This exercises the new Event.Features path:
// Chronos should see the real values change, not the constant 1.0 "presence"
// default (which yields a "stall").
func seedRisingFeature(t *testing.T, mem mnemos.Memory, runID string) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().Add(-48 * time.Hour)
	for day := 0; day < 14; day++ {
		ev := mnemos.Event{
			ID:       fmt.Sprintf("ef-%s-%d", runID, day),
			At:       base.Add(time.Duration(day) * time.Hour),
			Type:     "latency",
			Content:  "latency sample",
			Features: []float64{float64(day) * 10},
			RunID:    runID,
		}
		if err := mem.RememberEvent(ctx, ev); err != nil {
			t.Fatalf("RememberEvent: %v", err)
		}
	}
}

// TestEventFeatures_RealValuesChangeDetection verifies that a rising numeric
// Event.Feature is forwarded to Chronos and changes what it detects: because the
// series RISES (rather than the old constant 1.0 which reads as a "stall"),
// Chronos surfaces a non-stall pattern.
func TestEventFeatures_RealValuesChangeDetection(t *testing.T) {
	clearMnemosEnv(t)
	mem, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace=event_features"),
		mnemos.WithPassiveMode(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })

	ctx := context.Background()
	seedRisingFeature(t, mem, "run-feat")

	sigs, err := mem.Signals(ctx, mnemos.SignalQuery{RunID: "run-feat"})
	if err != nil {
		t.Fatalf("Signals: %v", err)
	}
	if len(sigs) == 0 {
		t.Fatal("expected at least one signal from the rising feature series, got none")
	}

	patterns := make([]string, 0, len(sigs))
	nonStall := false
	for _, s := range sigs {
		patterns = append(patterns, s.Pattern)
		if s.Pattern == "" {
			t.Error("signal has empty Pattern")
		}
		if s.Pattern != "stall" {
			nonStall = true
		}
	}
	t.Logf("detected patterns from rising feature series: %v", patterns)

	// The feature rises, so real values changed what Chronos saw: at least one
	// signal is NOT a "stall". If Chronos only ever emits stall here, treat that
	// as a detection regression for value-carrying events.
	if !nonStall {
		t.Errorf("all signals were %q (stall); rising feature values should surface a non-stall pattern: %v", "stall", patterns)
	}
}
