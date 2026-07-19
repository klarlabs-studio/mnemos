package main

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/workflow"
)

// `mnemos prune --narration [--dry-run]` deprecates already-stored claims that
// the current extraction filter classifies as conversational pollution rather
// than knowledge.
//
// The filters only run at ingest, so every claim captured before a filter
// existed is still stored and still recalls. A census of a real brain found
// nearly half of active claims were narration the older filters missed:
// lead-ins ("Confirmed absent from every downstream surface:"), section
// headers, evaluative fragments. Fixing extraction forward does nothing for
// them.
//
// This re-runs extract.IsJunk — byte-for-byte the same judgement the pipeline
// now applies — over stored claims, so the cleanup can never diverge from what
// ingest would have done. Dry-run by default; the write goes through the
// governed writer so each deprecation lands a claim_status_history row and is
// inspectable, never a raw UPDATE.
//
// It only DEPRECATES, never deletes: a mis-classified claim stays queryable and
// its transition is auditable and reversible, which raw deletion would not be.
func pruneNarration(dryRun bool, f Flags) {
	err := runJob("prune-narration", map[string]string{"dry_run": fmt.Sprint(dryRun)}, f.Verbose,
		func(ctx context.Context, job *workflow.Job, w *govwrite.Writer) error {
			conn := w.Conn()
			actor, actorErr := resolveActor(ctx, conn.Users, f.Actor)
			if actorErr != nil {
				return actorErr
			}
			if err := job.SetStatus("loading", ""); err != nil {
				return err
			}

			claims, err := conn.Claims.ListAll(ctx)
			if err != nil {
				return NewSystemError(err, "load claims")
			}

			junk := selectNarrationClaims(claims)

			fmt.Printf("active claims:   %d\n", countActive(claims))
			fmt.Printf("narration/junk:  %d (%.1f%% of active)\n", len(junk), pct(len(junk), countActive(claims)))
			for i, c := range junk {
				if i >= 8 {
					fmt.Printf("  … and %d more\n", len(junk)-8)
					break
				}
				fmt.Printf("  - %s\n", truncateClaim(c.Text))
			}

			if dryRun {
				fmt.Println("\n(dry run — nothing written; re-run without --dry-run to apply)")
				return nil
			}
			if len(junk) == 0 {
				fmt.Println("\nnothing to prune.")
				return nil
			}

			if err := job.SetStatus("saving", ""); err != nil {
				return err
			}
			// Deprecate through the governed writer in one batch so every
			// transition is attributed and audited. The reason names the tool
			// so a later reader can tell an automated cleanup from a human call.
			for i := range junk {
				junk[i].Status = domain.ClaimStatusDeprecated
			}
			if _, err := w.Claims(ctx, junk, govwrite.ClaimReason{
				Reason:    "Conversational pollution, not knowledge (prune --narration)",
				ChangedBy: actor,
			}); err != nil {
				return NewSystemError(err, "deprecate narration claims")
			}
			fmt.Printf("\ndeprecated %d narration claim(s); they remain queryable with --include-history.\n", len(junk))
			return nil
		})
	if err != nil {
		exitWithMnemosError(f.Verbose, err)
	}
}

// selectNarrationClaims returns the ACTIVE claims that the extraction filter
// classifies as conversational pollution. Only active claims are candidates: a
// contested or already-deprecated claim is either under review or already
// retired, and re-deprecating it would churn its history for no gain.
//
// Pure so the prune's core selection is testable without the store, the
// governed writer, or the CLI plumbing around it.
func selectNarrationClaims(claims []domain.Claim) []domain.Claim {
	var junk []domain.Claim
	for _, c := range claims {
		if c.Status != domain.ClaimStatusActive {
			continue
		}
		if extract.IsJunk(c.Text) {
			junk = append(junk, c)
		}
	}
	return junk
}

func countActive(claims []domain.Claim) int {
	n := 0
	for _, c := range claims {
		if c.Status == domain.ClaimStatusActive {
			n++
		}
	}
	return n
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(n) / float64(total)
}

// handlePrune routes `mnemos prune <--narration> [--dry-run]`. The kind is a
// required flag rather than a default so a bare `prune` never guesses at a
// destructive operation.
func handlePrune(args []string, f Flags) {
	target, err := parsePruneArgs(args)
	if err != nil {
		exitWithMnemosError(f.Verbose, err)
		return
	}
	switch target {
	case "narration":
		pruneNarration(f.DryRun, f)
	}
}

// parsePruneArgs resolves the prune target from its flags, separated from
// execution so the flag contract is testable without the process-exiting error
// path. A bare `prune` is an error, not a default: it must never guess at a
// destructive operation.
func parsePruneArgs(args []string) (target string, err error) {
	narration := false
	for _, a := range args {
		switch a {
		case "--narration":
			narration = true
		default:
			return "", NewUserError("unknown prune flag %q (want --narration [--dry-run])", a)
		}
	}
	if !narration {
		return "", NewUserError("prune requires a target: --narration (deprecate stored conversational pollution)")
	}
	return "narration", nil
}
