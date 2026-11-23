package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

func taskStateTestDSN(t *testing.T) string {
	t.Helper()
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "taskstate.db")
	t.Setenv("MNEMOS_DB_URL", dsn)
	return dsn
}

func TestTaskStateTools_RefuseUnlessOptedIn(t *testing.T) {
	taskStateTestDSN(t)
	t.Setenv("MNEMOS_TASK_STATE", "")
	ctx := context.Background()
	if _, err := mcpGetTaskState(ctx, mcpGetTaskStateInput{}); !errors.Is(err, errTaskStateDisabled) {
		t.Fatalf("get err = %v, want errTaskStateDisabled", err)
	}
	if _, err := mcpUpdateTaskState(ctx, "t", mcpUpdateTaskStateInput{Action: "admit", Kind: "goal", Text: "x"}); !errors.Is(err, errTaskStateDisabled) {
		t.Fatalf("update err = %v, want errTaskStateDisabled", err)
	}
}

func TestTaskStateTools_AdmitReadRemoveHandoff(t *testing.T) {
	dsn := taskStateTestDSN(t)
	t.Setenv("MNEMOS_TASK_STATE", "1")
	ctx := context.Background()

	steps := []struct {
		name    string
		in      mcpUpdateTaskStateInput
		check   func(t *testing.T, out mcpUpdateTaskStateOutput)
		wantErr bool
	}{
		{"admit goal", mcpUpdateTaskStateInput{Action: "admit", Kind: "goal", Text: "Ship the billing migration"},
			func(t *testing.T, out mcpUpdateTaskStateOutput) {
				if !out.Admitted {
					t.Fatalf("goal not admitted: %+v", out)
				}
			}, false},
		{"admit constraint", mcpUpdateTaskStateInput{Action: "admit", Kind: "constraint", Text: "Invoices stay in EUR"}, nil, false},
		{"admit question", mcpUpdateTaskStateInput{Action: "admit", Kind: "question", Text: "Is the cache warm?"}, nil, false},
		{"duplicate rejected", mcpUpdateTaskStateInput{Action: "admit", Kind: "goal", Text: "ship the billing migration"},
			func(t *testing.T, out mcpUpdateTaskStateOutput) {
				if out.Admitted {
					t.Fatal("duplicate admitted")
				}
			}, false},
		{"unknown claim refused", mcpUpdateTaskStateInput{Action: "admit", Kind: "hypothesis", Text: "x", RestsOn: []string{"nope"}}, nil, true},
		{"remove question", mcpUpdateTaskStateInput{Action: "remove", Text: "is the cache warm"},
			func(t *testing.T, out mcpUpdateTaskStateOutput) {
				if !out.Removed {
					t.Fatal("not removed")
				}
			}, false},
		{"unknown action", mcpUpdateTaskStateInput{Action: "promote"}, nil, true},
	}
	for _, s := range steps {
		out, err := mcpUpdateTaskState(ctx, "tester", s.in)
		if (err != nil) != s.wantErr {
			t.Fatalf("%s: err = %v, wantErr %v", s.name, err, s.wantErr)
		}
		if s.check != nil {
			s.check(t, out)
		}
	}

	got, err := mcpGetTaskState(ctx, mcpGetTaskStateInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || got.Items[0].Kind != "goal" || got.Session != "current" {
		t.Fatalf("state = %+v, want goal and constraint in the current session", got)
	}

	// Placement: with the experiment on, get_blocks lists task state first.
	mem, err := mnemos.New(mnemos.WithStorage(dsn), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.SetBlock(ctx, "claude-code", "a_persona", "terse"); err != nil {
		t.Fatal(err)
	}
	blocks, err := mcpGetBlocks(ctx, mem, mcpGetBlocksInput{Owner: "claude-code"})
	_ = mem.Close()
	if err != nil || len(blocks.Blocks) != 2 || blocks.Blocks[0].Label != "task_state:current" {
		t.Fatalf("blocks = %+v, %v; want task_state first", blocks, err)
	}

	// SessionEnd handoff: candidate claims in the store, block cleared.
	handOffTaskStateOnSessionEnd()
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	claims, err := conn.Claims.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidates := 0
	for _, c := range claims {
		if c.Lifecycle == domain.ClaimLifecycleCandidate {
			candidates++
		}
	}
	if candidates != 2 {
		t.Fatalf("candidates = %d, want 2", candidates)
	}
	if after, _ := mcpGetTaskState(ctx, mcpGetTaskStateInput{}); len(after.Items) != 0 {
		t.Fatalf("block not cleared after handoff: %+v", after)
	}
}
