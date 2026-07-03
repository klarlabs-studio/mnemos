package mnemos_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// TestRecordDecision_RoundTrip proves the public Decision API: RecordDecision
// persists through the governed write path (id generated, RiskLevel defaulted),
// and GetDecision / ListDecisions read it back. Runs on BOTH the memory and
// sqlite backends — the sqlite path once mis-generated its upsert SQL
// (excluded.failed_outcome_ vs _id), which only a real sqlite write catches.
func TestRecordDecision_RoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		opt  mnemos.Option
	}{
		{"memory", mnemos.WithStorage("memory://?namespace=dec_test")},
		{"sqlite", nil}, // filled per-run below (needs a temp path)
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearMnemosEnv(t)
			opt := tc.opt
			if opt == nil {
				opt = mnemos.WithSQLite(filepath.Join(t.TempDir(), "dec.db"))
			}
			runRecordDecisionRoundTrip(t, opt)
		})
	}
}

func runRecordDecisionRoundTrip(t *testing.T, storage mnemos.Option) {
	mem, err := mnemos.New(storage, mnemos.WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = mem.Close() }()

	ctx := context.Background()
	id, err := mem.RecordDecision(ctx, mnemos.Decision{
		Statement:    "roll back the payments deploy",
		Reasoning:    "error rate spiked right after the release",
		Alternatives: []string{"scale up", "wait and watch"},
		ChosenAt:     time.Now().UTC(),
		// RiskLevel intentionally empty → must default to "medium".
	})
	if err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	if id == "" {
		t.Fatal("RecordDecision returned an empty id")
	}

	got, err := mem.GetDecision(ctx, id)
	if err != nil {
		t.Fatalf("GetDecision: %v", err)
	}
	if got.Statement != "roll back the payments deploy" {
		t.Fatalf("statement round-trip wrong: %q", got.Statement)
	}
	if got.RiskLevel != "medium" {
		t.Fatalf("empty RiskLevel must default to medium, got %q", got.RiskLevel)
	}
	if len(got.Alternatives) != 2 {
		t.Fatalf("alternatives round-trip wrong: %+v", got.Alternatives)
	}

	list, err := mem.ListDecisions(ctx, 10)
	if err != nil {
		t.Fatalf("ListDecisions: %v", err)
	}
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("ListDecisions should return the one recorded decision, got %+v", list)
	}

	// Statement is required — a blank one is rejected before any write.
	if _, err := mem.RecordDecision(ctx, mnemos.Decision{Reasoning: "no statement"}); err == nil {
		t.Fatal("RecordDecision must reject a blank Statement")
	}
}
