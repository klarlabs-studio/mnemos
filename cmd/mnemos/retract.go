package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/retraction"
	"go.klarlabs.de/mnemos/internal/workflow"
)

// Event retraction on the CLI and over MCP (ADR 0037).
//
// Both surfaces — and REST, gRPC and the Go library — call retraction.Run,
// which validates the request once and calls govwrite.Writer.RetractEvent, the
// single implementation, and render its report through retraction.Report, so
// `mnemos retract-event --json`, the retract_episode tool and REST return the
// same document for the same store. Only argument parsing and authorization
// differ.

// retractionView is the wire form of a retraction report.
type retractionView = retraction.Report

// runRetraction is the one call path from a daemon surface to the retraction.
// runAllowed, when non-nil, confines the caller to its runs.
func runRetraction(ctx context.Context, w *govwrite.Writer, eventID, reason, actor string, runAllowed func(string) bool) (retractionView, error) {
	rep, err := retraction.Run(ctx, w.Conn().Events, w.RetractEvent, retraction.Request{
		EventID: eventID, Reason: reason, Actor: actor, RunAllowed: runAllowed,
	})
	if err != nil {
		return retractionView{}, err
	}
	return retraction.NewReport(rep), nil
}

// --- MCP ---

type mcpRetractEpisodeInput struct {
	EpisodeID string `json:"episode_id" jsonschema:"required,description=ID of the episode (event) to withdraw as evidence. The episode itself is kept; the retraction is recorded."`
	Reason    string `json:"reason,omitempty" jsonschema:"description=Why the episode is being retracted (recorded with the retraction and on every belief it retracts)"`
}

type mcpRetractEpisodeOutput = retractionView

func mcpRunRetractEpisode(ctx context.Context, actor string, input mcpRetractEpisodeInput) (mcpRetractEpisodeOutput, error) {
	gw, err := openWriter(ctx)
	if err != nil {
		return mcpRetractEpisodeOutput{}, err
	}
	defer closeWriter(gw)
	// A run-scoped token may retract only episodes of its runs.
	return runRetraction(ctx, gw, input.EpisodeID, input.Reason, actor, func(runID string) bool {
		return mcpRunAllowed(ctx, runID)
	})
}

// --- CLI ---

// handleRetractEvent implements `mnemos retract-event <id> [--reason <text>]`.
func handleRetractEvent(args []string, f Flags) {
	var id, reason string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--reason":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--reason requires a value"))
				return
			}
			reason = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--reason="):
			reason = strings.TrimPrefix(args[i], "--reason=")
		case strings.HasPrefix(args[i], "-"):
			exitWithMnemosError(false, NewUserError("unknown argument %q for retract-event", args[i]))
			return
		case id == "":
			id = args[i]
		default:
			exitWithMnemosError(false, NewUserError("retract-event takes one event id\n  mnemos retract-event <id> [--reason <text>]"))
			return
		}
	}
	if id == "" {
		exitWithMnemosError(false, NewUserError("retract-event requires an event id\n  mnemos retract-event <id> [--reason <text>]"))
		return
	}

	err := runJob("retract-event", map[string]string{"id": id}, f.Verbose, func(ctx context.Context, _ *workflow.Job, w *govwrite.Writer) error {
		actor, err := resolveActor(ctx, w.Conn().Users, f.Actor)
		if err != nil {
			return NewUserError("%v", err)
		}
		view, err := runRetraction(ctx, w, id, reason, actor, nil)
		if errors.Is(err, retraction.ErrEventNotFound) {
			return NewUserError("event %s not found", id)
		}
		if err != nil {
			return NewSystemError(err, "retract event %s", id)
		}
		if f.JSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(view)
		}
		printRetraction(view)
		return nil
	})
	exitWithMnemosError(f.Verbose, err)
}

func printRetraction(v retractionView) {
	if v.AlreadyRetracted {
		fmt.Printf("Event %s was already retracted at %s by %s; nothing changed.\n", v.EpisodeID, v.RetractedAt, v.RetractedBy)
	} else {
		fmt.Printf("Retracted event %s.\n", v.EpisodeID)
	}
	im := v.Impact
	fmt.Printf("  beliefs retracted (no live evidence left): %d %s\n", len(im.RetractedBeliefs), strings.Join(im.RetractedBeliefs, " "))
	for _, r := range im.Rescored {
		fmt.Printf("  belief %s kept on %d other episode(s); trust %.3f -> %.3f\n", r.ID, r.RemainingEvidence, r.TrustBefore, r.TrustAfter)
	}
	if len(im.AlreadyDeprecated) > 0 {
		fmt.Printf("  already deprecated, left as is: %s\n", strings.Join(im.AlreadyDeprecated, " "))
	}
	if len(im.Associations) > 0 {
		fmt.Printf("  associations touching retracted beliefs (kept): %s\n", strings.Join(im.Associations, " "))
	}
	for _, d := range im.Decisions {
		fmt.Printf("  decision %s cited retracted belief(s) %s: review it (not judged wrong)\n", d.DecisionID, strings.Join(d.Beliefs, " "))
	}
	if len(im.OutcomeEdges) > 0 {
		fmt.Printf("  outcome edges onto retracted beliefs (kept): %s\n", strings.Join(im.OutcomeEdges, " "))
	}
	for _, u := range im.Uncertainty {
		fmt.Printf("  note: %s\n", u)
	}
}
