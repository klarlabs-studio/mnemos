package main

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/workflow"
)

// `mnemos recompute-contested [--dry-run]` clears contested status from claims
// the CURRENT heuristic would no longer flag.
//
// Contested is assigned at extraction time, so a claim marked by an older,
// looser rule keeps that status forever — the fix only applies to new writes.
// That gap was real: the opposite-polarity rule once required just two shared
// tokens with no same-subject check, which left a production brain 50%
// contested. Contested claims are demoted at recall, so a majority-contested
// brain has a degraded tier and no way to recover it.
//
// This is the contested analogue of `relate --prune-stale`: re-derive against
// today's rules and drop what they no longer produce. It re-runs the real
// heuristic (extract.ContestedIDs) rather than reimplementing it, so the pass
// can never diverge from what extraction would do now.
//
// It only ever CLEARS (contested → active), never adds. Adding would silently
// demote claims at recall; a maintenance pass should recover signal, not
// destroy it. Claims the heuristic still flags are left untouched.
func recomputeContested(dryRun bool, f Flags) {
	err := runJob("recompute-contested", map[string]string{"dry_run": fmt.Sprint(dryRun)}, f.Verbose,
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
			links, err := conn.Claims.ListAllEvidence(ctx)
			if err != nil {
				return NewSystemError(err, "load claim evidence")
			}
			events, err := conn.Events.ListAll(ctx)
			if err != nil {
				return NewSystemError(err, "load events")
			}

			if err := job.SetStatus("relating", ""); err != nil {
				return err
			}
			cleared := recomputeContestedClaims(claims, links, events)

			contested := 0
			for _, c := range claims {
				if c.Status == domain.ClaimStatusContested {
					contested++
				}
			}
			fmt.Printf("contested claims: %d\n", contested)
			fmt.Printf("no longer flagged: %d (%.1f%%) — would return to active\n",
				len(cleared), pct(len(cleared), contested))
			for i, c := range cleared {
				if i >= 6 {
					fmt.Printf("  … and %d more\n", len(cleared)-6)
					break
				}
				fmt.Printf("  - %s\n", truncateClaim(c.Text))
			}

			if dryRun {
				fmt.Println("\n(dry run — nothing written; re-run without --dry-run to apply)")
				return nil
			}
			if len(cleared) == 0 {
				fmt.Println("\nnothing to recompute.")
				return nil
			}

			if err := job.SetStatus("saving", ""); err != nil {
				return err
			}
			for i := range cleared {
				cleared[i].Status = domain.ClaimStatusActive
			}
			if _, err := w.Claims(ctx, cleared, govwrite.ClaimReason{
				Reason:    "Not contested under the current heuristic (recompute-contested)",
				ChangedBy: actor,
			}); err != nil {
				return NewSystemError(err, "clear contested status")
			}
			fmt.Printf("\nreturned %d claim(s) to active; each transition is recorded in claim_status_history.\n", len(cleared))
			return nil
		})
	if err != nil {
		exitWithMnemosError(f.Verbose, err)
	}
}

// recomputeContestedClaims returns the currently-contested claims that today's
// heuristic would NOT flag.
//
// The heuristic is pairwise within an extraction batch, so the claims are
// regrouped by the run that produced them before re-deriving — comparing a
// claim against the whole store would flag pairs extraction never saw together
// and produce a meaningless answer. Claims with no evidence link fall into a
// bucket of their own and are simply left alone: without knowing their batch we
// cannot say what the heuristic would do.
func recomputeContestedClaims(claims []domain.Claim, links []domain.ClaimEvidence, events []domain.Event) []domain.Claim {
	runOfEvent := make(map[string]string, len(events))
	for _, e := range events {
		runOfEvent[e.ID] = e.RunID
	}
	runOfClaim := make(map[string]string, len(claims))
	for _, l := range links {
		if _, seen := runOfClaim[l.ClaimID]; seen {
			continue // first evidence link wins; a claim's batch is where it was born
		}
		if run, ok := runOfEvent[l.EventID]; ok {
			runOfClaim[l.ClaimID] = run
		}
	}

	byRun := make(map[string][]domain.Claim)
	for _, c := range claims {
		run, ok := runOfClaim[c.ID]
		if !ok {
			continue // unknown batch — leave its status alone
		}
		byRun[run] = append(byRun[run], c)
	}

	var cleared []domain.Claim
	for _, group := range byRun {
		stillContested := extract.ContestedIDs(group)
		for _, c := range group {
			if c.Status != domain.ClaimStatusContested {
				continue
			}
			if _, still := stillContested[c.ID]; !still {
				cleared = append(cleared, c)
			}
		}
	}
	return cleared
}
