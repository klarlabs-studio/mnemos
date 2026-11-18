package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/retraction"
)

// The unretract_episode tool and `mnemos unretract-event` share one call path:
// an un-retraction made over MCP reads back identically through the CLI path,
// marked as a repeat, and the belief the retraction deprecated is back.
func TestUnretract_MCPAndCLIShareOneOperation(t *testing.T) {
	setIsolatedDB(t)
	ctx := context.Background()
	remembered, err := mcpRunRemember(ctx, "a", mcpRememberInput{Text: "the payments queue is drained hourly", RunID: "tenant:A"})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if _, err := mcpRunRetractEpisode(ctx, "agent-1", mcpRetractEpisodeInput{EpisodeID: remembered.EventID, Reason: "wrong runbook"}); err != nil {
		t.Fatalf("retract_episode: %v", err)
	}

	viaMCP, err := mcpRunUnretractEpisode(ctx, "agent-2", mcpUnretractEpisodeInput{EpisodeID: remembered.EventID, Reason: "runbook was right"})
	if err != nil {
		t.Fatalf("unretract_episode: %v", err)
	}
	if viaMCP.AlreadyUnretracted || viaMCP.RetractedBy != "agent-1" || viaMCP.UnretractedBy != "agent-2" {
		t.Fatalf("report = %+v, want a first un-retraction by agent-2 of agent-1's retraction", viaMCP)
	}
	if got := viaMCP.Impact.Restored; len(got) != 1 || got[0].ID != remembered.ClaimID {
		t.Fatalf("restored = %+v, want [%s]", got, remembered.ClaimID)
	}

	gw, err := openWriter(ctx)
	if err != nil {
		t.Fatalf("openWriter: %v", err)
	}
	defer closeWriter(gw)
	viaCLI, err := runUnretraction(ctx, gw, remembered.EventID, "again", "operator", nil)
	if err != nil {
		t.Fatalf("CLI path: %v", err)
	}
	if !viaCLI.AlreadyUnretracted {
		t.Error("CLI path did not see the MCP un-retraction")
	}
	viaCLI.AlreadyUnretracted = false
	if !reflect.DeepEqual(viaCLI, viaMCP) {
		t.Errorf("CLI view %+v differs from MCP view %+v", viaCLI, viaMCP)
	}

	got, err := gw.Conn().Claims.ListByIDs(ctx, []string{remembered.ClaimID})
	if err != nil || len(got) != 1 || got[0].Status != domain.ClaimStatusActive {
		t.Fatalf("read back = %+v (%v), want active", got, err)
	}
	history, err := gw.Conn().Claims.ListStatusHistoryByClaimID(ctx, remembered.ClaimID)
	if err != nil || len(history) == 0 {
		t.Fatalf("history: %v", err)
	}
	if last := history[len(history)-1]; !strings.Contains(last.Reason, "runbook was right") || last.ChangedBy != "agent-2" {
		t.Errorf("last transition = %+v, want the MCP caller's reason and identity", last)
	}
}

func TestUnretract_MCPRefusals(t *testing.T) {
	setIsolatedDB(t)
	ctx := context.Background()
	remembered, err := mcpRunRemember(ctx, "a", mcpRememberInput{Text: "the payments queue is drained hourly", RunID: "tenant:A"})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if _, err := mcpRunUnretractEpisode(ctx, "a", mcpUnretractEpisodeInput{EpisodeID: "ev_missing"}); !errors.Is(err, retraction.ErrEventNotFound) {
		t.Errorf("unknown episode: err = %v, want ErrEventNotFound", err)
	}
	if _, err := mcpRunUnretractEpisode(ctx, "a", mcpUnretractEpisodeInput{}); !errors.Is(err, retraction.ErrEventIDRequired) {
		t.Errorf("no episode id: err = %v, want ErrEventIDRequired", err)
	}
	if _, err := mcpRunUnretractEpisode(ctx, "a", mcpUnretractEpisodeInput{EpisodeID: remembered.EventID}); !errors.Is(err, retraction.ErrNotRetracted) {
		t.Errorf("never retracted: err = %v, want ErrNotRetracted", err)
	}
}
