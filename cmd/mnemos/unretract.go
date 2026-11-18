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

// Event un-retraction on the CLI and over MCP (ADR 0037 §8).
//
// Like retraction, every surface calls retraction.Unretract, which validates
// the request once (id, existence, run scope, and that the episode was ever
// retracted) and calls govwrite.Writer.UnretractEvent, and renders its report
// through retraction.UnretractionReport, so `mnemos unretract-event --json`,
// the unretract_episode tool and REST return the same document.

// unretractionView is the wire form of an un-retraction report.
type unretractionView = retraction.UnretractionReport

// runUnretraction is the one call path from a daemon surface to the
// un-retraction. runAllowed, when non-nil, confines the caller to its runs.
func runUnretraction(ctx context.Context, w *govwrite.Writer, eventID, reason, actor string, runAllowed func(string) bool) (unretractionView, error) {
	conn := w.Conn()
	rep, err := retraction.Unretract(ctx, conn.Events, conn.Journal, w.UnretractEvent, retraction.Request{
		EventID: eventID, Reason: reason, Actor: actor, RunAllowed: runAllowed,
	})
	if err != nil {
		return unretractionView{}, err
	}
	return retraction.NewUnretractionReport(rep), nil
}

// --- MCP ---

type mcpUnretractEpisodeInput struct {
	EpisodeID string `json:"episode_id" jsonschema:"required,description=ID of a retracted episode (event) to count as evidence again. The retraction stays on record; the un-retraction is recorded next to it."`
	Reason    string `json:"reason,omitempty" jsonschema:"description=Why the retraction is being cleared (recorded with the un-retraction and on every belief it restores)"`
}

type mcpUnretractEpisodeOutput = unretractionView

func mcpRunUnretractEpisode(ctx context.Context, actor string, input mcpUnretractEpisodeInput) (mcpUnretractEpisodeOutput, error) {
	gw, err := openWriter(ctx)
	if err != nil {
		return mcpUnretractEpisodeOutput{}, err
	}
	defer closeWriter(gw)
	// A run-scoped token may un-retract only episodes of its runs.
	return runUnretraction(ctx, gw, input.EpisodeID, input.Reason, actor, func(runID string) bool {
		return mcpRunAllowed(ctx, runID)
	})
}

// --- CLI ---

const unretractUsage = "  mnemos unretract-event <id> [--reason <text>]"

// handleUnretractEvent implements `mnemos unretract-event <id> [--reason <text>]`.
func handleUnretractEvent(args []string, f Flags) {
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
			exitWithMnemosError(false, NewUserError("unknown argument %q for unretract-event", args[i]))
			return
		case id == "":
			id = args[i]
		default:
			exitWithMnemosError(false, NewUserError("unretract-event takes one event id\n"+unretractUsage))
			return
		}
	}
	if id == "" {
		exitWithMnemosError(false, NewUserError("unretract-event requires an event id\n"+unretractUsage))
		return
	}

	err := runJob("unretract-event", map[string]string{"id": id}, f.Verbose, func(ctx context.Context, _ *workflow.Job, w *govwrite.Writer) error {
		actor, err := resolveActor(ctx, w.Conn().Users, f.Actor)
		if err != nil {
			return NewUserError("%v", err)
		}
		view, err := runUnretraction(ctx, w, id, reason, actor, nil)
		switch {
		case errors.Is(err, retraction.ErrEventNotFound):
			return NewUserError("event %s not found", id)
		case errors.Is(err, retraction.ErrNotRetracted):
			return NewUserError("event %s was never retracted; nothing to un-retract", id)
		case err != nil:
			return NewSystemError(err, "un-retract event %s", id)
		}
		if f.JSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(view)
		}
		printUnretraction(view)
		return nil
	})
	exitWithMnemosError(f.Verbose, err)
}

func printUnretraction(v unretractionView) {
	if v.AlreadyUnretracted {
		fmt.Printf("Event %s was already un-retracted at %s by %s; nothing changed.\n", v.EpisodeID, v.UnretractedAt, v.UnretractedBy)
	} else {
		fmt.Printf("Un-retracted event %s (clearing the retraction of %s by %s).\n", v.EpisodeID, v.RetractedAt, v.RetractedBy)
	}
	im := v.Impact
	for _, r := range im.Restored {
		fmt.Printf("  belief %s restored to %s\n", r.ID, r.Status)
	}
	for _, s := range im.Skipped {
		fmt.Printf("  belief %s left as is: %s\n", s.ID, s.Reason)
	}
	for _, r := range im.Relinked {
		fmt.Printf("  belief %s regained this episode as evidence; trust %.3f -> %.3f\n", r.ID, r.TrustBefore, r.TrustAfter)
	}
	for _, u := range im.Uncertainty {
		fmt.Printf("  note: %s\n", u)
	}
}
