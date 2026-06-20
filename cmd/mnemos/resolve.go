package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
)

// handleResolve implements two operator-driven primitives for the
// truth layer:
//
//	mnemos resolve <winner> --over       <loser>      [--reason "..."]
//	mnemos resolve <new>    --supersedes <old>        [--reason "..."]
//
// `--over` is the original "this contradicts that, pick a winner"
// flow: winner → resolved, loser → deprecated, both transitions
// audited. Use it for contradictions where one claim is true and the
// other is false.
//
// `--supersedes` is the temporal-validity primitive added in v0.8:
// the new claim takes effect, the old one is closed (valid_to = the
// new claim's valid_from). The old claim is NOT marked deprecated —
// it remained true while it was true, and history queries (`--at`,
// `--include-history`) still surface it. Use it for facts that
// changed over time, like job titles or chosen tools.
//
// Explicitly NOT implemented: automatic resolution by recency or
// confidence. That path is risky and would undermine the project's
// "surface contradictions, let humans judge" stance.
func handleResolve(args []string, f Flags) {
	if len(args) < 1 {
		exitWithMnemosError(false, NewUserError("resolve requires a claim id\n  mnemos resolve <winner-id> --over <loser-id> [--reason \"...\"]\n  mnemos resolve <new-id> --supersedes <old-id> [--reason \"...\"]"))
		return
	}
	primaryID := args[0]
	args = args[1:]

	loserID := ""
	supersededID := ""
	reason := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--over":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--over requires a claim id"))
				return
			}
			loserID = args[i+1]
			i++
		case "--supersedes":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--supersedes requires a claim id"))
				return
			}
			supersededID = args[i+1]
			i++
		case "--reason":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--reason requires a string"))
				return
			}
			reason = args[i+1]
			i++
		default:
			exitWithMnemosError(false, NewUserError("unknown resolve flag %q", args[i]))
			return
		}
	}

	if strings.TrimSpace(primaryID) == "" {
		exitWithMnemosError(false, NewUserError("primary claim id required"))
		return
	}
	if loserID == "" && supersededID == "" {
		exitWithMnemosError(false, NewUserError("either --over <loser-id> or --supersedes <old-id> is required"))
		return
	}
	if loserID != "" && supersededID != "" {
		exitWithMnemosError(false, NewUserError("--over and --supersedes are mutually exclusive (contradiction vs temporal supersession)"))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	w, err := openWriter(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open database"))
		return
	}
	defer closeWriter(w)

	if supersededID != "" {
		runSupersession(ctx, w, primaryID, supersededID, reason, f.Actor)
		return
	}
	runContradictionResolution(ctx, w, primaryID, loserID, reason, f.Actor)
}

// runContradictionResolution preserves the v0.6 `--over` behavior:
// pick a winner, mark the other deprecated, audit the transition.
func runContradictionResolution(ctx context.Context, w *govwrite.Writer, winnerID, loserID, reason, actorFlag string) {
	if winnerID == loserID {
		exitWithMnemosError(false, NewUserError("winner and loser must be different claims"))
		return
	}
	if reason == "" {
		reason = "operator resolution via mnemos resolve"
	}
	conn := w.Conn()
	claimRepo := conn.Claims
	found, err := claimRepo.ListByIDs(ctx, []string{winnerID, loserID})
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "look up claims"))
		return
	}
	byID := make(map[string]domain.Claim, len(found))
	for _, c := range found {
		byID[c.ID] = c
	}
	winner, winnerOK := byID[winnerID]
	loser, loserOK := byID[loserID]
	if !winnerOK {
		exitWithMnemosError(false, NewUserError("winner claim %q not found", winnerID))
		return
	}
	if !loserOK {
		exitWithMnemosError(false, NewUserError("loser claim %q not found", loserID))
		return
	}

	actor, err := resolveActor(ctx, conn.Users, actorFlag)
	if err != nil {
		exitWithMnemosError(false, err)
		return
	}

	winner.Status = domain.ClaimStatusResolved
	loser.Status = domain.ClaimStatusDeprecated

	if _, err := w.Claims(ctx, []domain.Claim{winner, loser}, govwrite.ClaimReason{Reason: reason, ChangedBy: actor}); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "persist resolution"))
		return
	}

	fmt.Printf("resolved: %s (%s -> resolved) over %s (%s -> deprecated)\n",
		winner.ID, winner.Type, loser.ID, loser.Type)
	fmt.Printf("reason: %s by=%s\n", reason, actor)
}

// runSupersession is the v0.8 temporal-validity primitive: close
// the old claim's valid interval at the new claim's valid_from. The
// old claim keeps its status (it remained true while it was true);
// only valid_to changes. The audit trail goes via
// claim_status_history with the provided reason so reviewers can
// see who said "this superseded that and when".
func runSupersession(ctx context.Context, w *govwrite.Writer, newID, oldID, reason, actorFlag string) {
	if newID == oldID {
		exitWithMnemosError(false, NewUserError("new and old must be different claims"))
		return
	}
	if reason == "" {
		reason = "operator supersession via mnemos resolve"
	}
	conn := w.Conn()
	claimRepo := conn.Claims
	found, err := claimRepo.ListByIDs(ctx, []string{newID, oldID})
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "look up claims"))
		return
	}
	byID := make(map[string]domain.Claim, len(found))
	for _, c := range found {
		byID[c.ID] = c
	}
	newClaim, newOK := byID[newID]
	oldClaim, oldOK := byID[oldID]
	if !newOK {
		exitWithMnemosError(false, NewUserError("new claim %q not found", newID))
		return
	}
	if !oldOK {
		exitWithMnemosError(false, NewUserError("old claim %q not found", oldID))
		return
	}

	actor, err := resolveActor(ctx, conn.Users, actorFlag)
	if err != nil {
		exitWithMnemosError(false, err)
		return
	}

	cutoff := newClaim.ValidFrom
	if cutoff.IsZero() {
		cutoff = newClaim.CreatedAt
	}
	if cutoff.IsZero() {
		cutoff = time.Now().UTC()
	}

	if err := w.SetValidity(ctx, oldClaim.ID, cutoff); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "set valid_to on superseded claim"))
		return
	}
	// Status history capture: append a transition row even though
	// status itself doesn't change, so the audit trail records who
	// performed the supersession. We re-upsert the same status with
	// the reason to trigger the history insert in upsertWithReason.
	if _, err := w.Claims(ctx, []domain.Claim{oldClaim}, govwrite.ClaimReason{Reason: reason, ChangedBy: actor}); err != nil {
		// Non-fatal: the supersession itself succeeded. Surface as a
		// warning so the operator knows the audit row didn't land.
		warn := icon("⚠️", "(!)")
		fmt.Fprintf(os.Stderr, "  %s audit row for supersession failed: %v\n", warn, err)
	}

	fmt.Printf("superseded: %s closed at %s (replaced by %s)\n",
		oldClaim.ID, cutoff.UTC().Format("2006-01-02"), newClaim.ID)
	fmt.Printf("reason: %s by=%s\n", reason, actor)
}
