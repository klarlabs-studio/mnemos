package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// handleClaim routes `mnemos claim <subcommand> ...`.
func handleClaim(args []string, _ Flags) {
	if len(args) == 0 {
		exitWithMnemosError(false, NewUserError("claim requires a subcommand: record | salience"))
		return
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "record":
		handleClaimRecord(rest)
	case "salience":
		handleClaimSalience(rest)
	default:
		exitWithMnemosError(false, NewUserError("unknown claim subcommand %q (want record | salience)", sub))
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
		salience   = fs.Float64("salience", -1, "explicit salience / stakes weight in [0,1] (ADR 0013 §4); omit for neutral")
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
	// -1 is the "unset" sentinel; any value in [0,1] is an explicit override.
	if *salience >= 0 {
		if *salience > 1 {
			exitWithMnemosError(false, NewUserError("--salience must be in [0, 1]"))
			return
		}
		item.Salience = *salience
		item.HasSalience = true
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

// handleClaimSalience routes `mnemos claim salience set <claim-id> <0..1>` — the
// explicit-override path for the unified salience / stakes weight (ADR 0013 §4).
// It writes the reserved "salience" confidence component on an EXISTING claim,
// leaving the claim's trust score untouched (salience is inert to trust). Reuses
// the ports.BeliefCreditWriter component-writer capability — no schema change.
func handleClaimSalience(args []string) {
	if len(args) < 1 || args[0] != "set" {
		exitWithMnemosError(false, NewUserError("claim salience requires: set <claim-id> <0..1>"))
		return
	}
	rest := args[1:]
	if len(rest) != 2 {
		exitWithMnemosError(false, NewUserError("usage: claim salience set <claim-id> <0..1>"))
		return
	}
	claimID := strings.TrimSpace(rest[0])
	if claimID == "" {
		exitWithMnemosError(false, NewUserError("claim id is required"))
		return
	}
	val, err := strconv.ParseFloat(rest[1], 64)
	if err != nil || val < 0 || val > 1 {
		exitWithMnemosError(false, NewUserError("salience must be a float in [0, 1]"))
		return
	}

	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer func() { _ = conn.Close() }()

	writer, ok := conn.Claims.(ports.BeliefCreditWriter)
	if !ok {
		exitWithMnemosError(false, NewUserError("this store backend does not support setting confidence components"))
		return
	}
	claims, err := conn.Claims.ListByIDs(ctx, []string{claimID})
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "load claim %s", claimID))
		return
	}
	if len(claims) == 0 {
		exitWithMnemosError(false, NewUserError("no claim with id %q", claimID))
		return
	}
	c := claims[0]
	merged := make(map[string]float64, len(c.ConfidenceComponents)+1)
	for k, v := range c.ConfidenceComponents {
		merged[k] = v
	}
	merged[domain.SalienceComponentKey] = val
	if err := writer.ApplyBeliefCredit(ctx, claimID, merged, c.TrustScore); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "set salience for %s", claimID))
		return
	}
	fmt.Printf("set salience=%.3f on claim %s\n", val, claimID)
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
