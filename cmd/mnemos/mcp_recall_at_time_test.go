package main

import (
	"context"
	"path/filepath"
	"testing"
)

// recall_at_time's recordedAsOf (ADR 0036) is optional and validated: a
// malformed value is the caller's error, and a well-formed one answers.
func TestMCPRecallAtTime_RecordedAsOf(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "rat.db"))
	ctx := context.Background()

	_, err := mcpRunRecallAtTime(ctx, mcpRecallAtTimeInput{
		Question: "what changed", AsOf: "2026-11-17T00:00:00Z", RecordedAsOf: "last tuesday",
	})
	if err == nil || !isUserInputError(err) {
		t.Fatalf("malformed recordedAsOf: err = %v, want a user input error", err)
	}

	for _, recorded := range []string{"", "2026-11-10T00:00:00Z"} {
		if _, err := mcpRunRecallAtTime(ctx, mcpRecallAtTimeInput{
			Question: "what changed", AsOf: "2026-11-17T00:00:00Z", RecordedAsOf: recorded,
		}); err != nil {
			t.Fatalf("recordedAsOf=%q: %v", recorded, err)
		}
	}
}
