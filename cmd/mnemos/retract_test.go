package main

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

// The retract_episode tool and `mnemos retract-event` share one call path, so a
// retraction made over MCP reads back identically through the CLI path: the
// same record, the same impact, marked as a repeat.
func TestRetract_MCPAndCLIShareOneOperation(t *testing.T) {
	setIsolatedDB(t)
	ctx := context.Background()
	remembered, err := mcpRunRemember(ctx, "a", mcpRememberInput{Text: "the payments queue is drained hourly", RunID: "tenant:A"})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}

	viaMCP, err := mcpRunRetractEpisode(ctx, "agent-1", mcpRetractEpisodeInput{EpisodeID: remembered.EventID, Reason: "wrong runbook"})
	if err != nil {
		t.Fatalf("retract_episode: %v", err)
	}
	if viaMCP.AlreadyRetracted {
		t.Fatal("first retraction reported as a repeat")
	}
	if got := viaMCP.Impact.RetractedBeliefs; len(got) != 1 || got[0] != remembered.ClaimID {
		t.Fatalf("retracted beliefs = %v, want [%s]", got, remembered.ClaimID)
	}

	gw, err := openWriter(ctx)
	if err != nil {
		t.Fatalf("openWriter: %v", err)
	}
	defer closeWriter(gw)
	viaCLI, err := runRetraction(ctx, gw, remembered.EventID, "again", "operator")
	if err != nil {
		t.Fatalf("CLI path: %v", err)
	}
	if !viaCLI.AlreadyRetracted {
		t.Error("CLI path did not see the MCP retraction")
	}
	viaCLI.AlreadyRetracted = false
	if !reflect.DeepEqual(viaCLI, viaMCP) {
		t.Errorf("CLI view %+v differs from MCP view %+v", viaCLI, viaMCP)
	}

	got, err := gw.Conn().Claims.ListByIDs(ctx, []string{remembered.ClaimID})
	if err != nil || len(got) != 1 {
		t.Fatalf("read back: %v %v", got, err)
	}
	if got[0].Status != domain.ClaimStatusDeprecated {
		t.Errorf("status = %s, want deprecated", got[0].Status)
	}
	history, err := gw.Conn().Claims.ListStatusHistoryByClaimID(ctx, remembered.ClaimID)
	if err != nil || len(history) == 0 {
		t.Fatalf("history: %v", err)
	}
	last := history[len(history)-1]
	if !strings.Contains(last.Reason, "wrong runbook") || last.ChangedBy != "agent-1" {
		t.Errorf("last transition = %+v, want the MCP caller's reason and identity", last)
	}
}

func TestRetract_MCPRejectsUnknownAndEmptyEpisodes(t *testing.T) {
	setIsolatedDB(t)
	ctx := context.Background()
	if _, err := mcpRunRetractEpisode(ctx, "a", mcpRetractEpisodeInput{EpisodeID: "ev_missing"}); err == nil {
		t.Error("retracting an unknown episode succeeded")
	}
	if _, err := mcpRunRetractEpisode(ctx, "a", mcpRetractEpisodeInput{}); err == nil {
		t.Error("retracting without an episode id succeeded")
	}
}
