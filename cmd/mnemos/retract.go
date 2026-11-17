package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/workflow"
)

// Event retraction on the CLI and over MCP (ADR 0036).
//
// Both surfaces call runRetraction, which calls govwrite.Writer.RetractEvent —
// the single implementation — and render its report through retractionView, so
// `mnemos retract-event --json` and the retract_episode tool return the same
// document for the same store. Only argument parsing and authorization differ.

// retractionView is the wire form of a retraction report, shared by the CLI's
// JSON output and the MCP tool. Times are RFC 3339 strings.
type retractionView struct {
	EpisodeID        string                  `json:"episode_id"`
	RetractedAt      string                  `json:"retracted_at"`
	RetractedBy      string                  `json:"retracted_by"`
	Reason           string                  `json:"reason,omitempty"`
	AlreadyRetracted bool                    `json:"already_retracted"`
	Impact           domain.RetractionImpact `json:"impact"`
}

func newRetractionView(rep domain.EventRetraction) retractionView {
	return retractionView{
		EpisodeID:        rep.EventID,
		RetractedAt:      rep.RetractedAt.UTC().Format(time.RFC3339Nano),
		RetractedBy:      rep.RetractedBy,
		Reason:           rep.Reason,
		AlreadyRetracted: rep.AlreadyRetracted,
		Impact:           rep.Impact,
	}
}

// runRetraction is the one call path from a surface to the retraction.
func runRetraction(ctx context.Context, w *govwrite.Writer, eventID, reason, actor string) (retractionView, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return retractionView{}, userInputError(errors.New("an episode id is required"))
	}
	rep, err := w.RetractEvent(ctx, eventID, domain.ChangeNote{Reason: strings.TrimSpace(reason), ChangedBy: actor})
	if err != nil {
		return retractionView{}, err
	}
	return newRetractionView(rep), nil
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
	if id := strings.TrimSpace(input.EpisodeID); id != "" {
		ev, err := gw.Conn().Events.GetByID(ctx, id)
		if err != nil {
			return mcpRetractEpisodeOutput{}, fmt.Errorf("episode %s not found", id)
		}
		if !mcpRunAllowed(ctx, ev.RunID) {
			return mcpRetractEpisodeOutput{}, fmt.Errorf("episode %s not found", id)
		}
	}
	return runRetraction(ctx, gw, input.EpisodeID, input.Reason, actor)
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
		view, err := runRetraction(ctx, w, id, reason, actor)
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
