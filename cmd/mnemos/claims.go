package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"go.klarlabs.de/mnemos"
)

// handleClaim routes `mnemos claim <subcommand> ...`.
func handleClaim(args []string, _ Flags) {
	if len(args) == 0 {
		exitWithMnemosError(false, NewUserError("claim requires a subcommand: record"))
		return
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "record":
		handleClaimRecord(rest)
	default:
		exitWithMnemosError(false, NewUserError("unknown claim subcommand %q (want record)", sub))
	}
}

// handleClaimRecord persists a pre-built claim via the public
// [mnemos.Memory.RememberClaim] API. This is the CLI counterpart of
// the library's Mode 3 input (agent-supplied claims); the MCP path
// is the `remember` tool.
func handleClaimRecord(args []string) {
	fs := flag.NewFlagSet("claim record", flag.ContinueOnError)
	var (
		text       = fs.String("text", "", "claim text (required)")
		claimType  = fs.String("type", "fact", "claim type: fact | hypothesis | decision | test_result")
		confidence = fs.Float64("confidence", 1.0, "agent confidence in [0, 1]")
		runID      = fs.String("run-id", "", "optional run id for scoped recall")
		eventIDs   = fs.String("event-ids", "", "comma-separated event ids to link as evidence")
		validFrom  = fs.String("valid-from", "", "RFC3339 timestamp when the claim first became true")
		validUntil = fs.String("valid-until", "", "RFC3339 timestamp when the claim stopped being true")
		global     = fs.Bool("global", false, "tag the claim to always float up to the central brain via `mnemos float-back`")
	)
	if err := fs.Parse(args); err != nil {
		exitWithMnemosError(false, NewUserError("%v", err))
		return
	}
	if strings.TrimSpace(*text) == "" {
		exitWithMnemosError(false, NewUserError("--text is required"))
		return
	}

	item := mnemos.ClaimItem{
		Text:       *text,
		Type:       *claimType,
		Confidence: *confidence,
		RunID:      *runID,
		Global:     *global,
	}
	if *eventIDs != "" {
		for id := range strings.SplitSeq(*eventIDs, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				item.EventIDs = append(item.EventIDs, id)
			}
		}
	}
	if s := strings.TrimSpace(*validFrom); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			exitWithMnemosError(false, NewUserError("--valid-from must be RFC3339: %v", err))
			return
		}
		item.ValidFrom = t
	}
	if s := strings.TrimSpace(*validUntil); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			exitWithMnemosError(false, NewUserError("--valid-until must be RFC3339: %v", err))
			return
		}
		item.ValidUntil = t
	}

	ctx := context.Background()
	actor := resolveCLIActor()

	mem, err := newLibraryMemory(ctx, actor)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open memory"))
		return
	}
	defer func() { _ = mem.Close() }()

	id, err := mem.RememberClaim(ctx, item)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "record claim"))
		return
	}
	fmt.Printf("recorded claim %s\n", id)
}

// resolveCLIActor reads MNEMOS_USER_ID for attribution, falling back
// to a CLI-default if unset. Mirrors the MCP actor resolution but
// uses a CLI-specific sentinel so audit can distinguish the source.
func resolveCLIActor() string {
	if v := os.Getenv("MNEMOS_USER_ID"); v != "" {
		return v
	}
	return "<cli>"
}
