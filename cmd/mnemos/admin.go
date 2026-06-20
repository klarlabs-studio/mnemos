package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/embedding"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/trust"
	"go.klarlabs.de/mnemos/internal/workflow"
)

// resetCounts captures what was removed during a reset for the user-facing
// summary. Zero values are still printed so users see exactly what changed.
type resetCounts struct {
	Claims        int64
	Evidence      int64
	StatusHistory int64
	Relationships int64
	Embeddings    int64
	Events        int64
}

func handleReset(args []string, f Flags) {
	keepEvents := false
	for _, a := range args {
		switch a {
		case "--keep-events":
			keepEvents = true
		default:
			fmt.Fprintf(os.Stderr, "error: unknown argument %q for reset\n", a)
			fmt.Fprintln(os.Stderr, "  mnemos reset [--keep-events] [--yes]")
			os.Exit(int(ExitUsage))
		}
	}

	if !f.Yes {
		desc := "all events, claims, relationships, and embeddings"
		if keepEvents {
			desc = "all claims, relationships, and embeddings (events kept)"
		}
		if !confirm(fmt.Sprintf("This will delete %s from %s. Continue?", desc, resolveDBPath())) {
			fmt.Println("aborted")
			os.Exit(int(ExitSuccess))
		}
	}

	err := runJob("reset", map[string]string{"keep_events": fmt.Sprintf("%t", keepEvents)}, f.Verbose, func(ctx context.Context, _ *workflow.Job, w *govwrite.Writer) error {
		gc, err := w.Reset(ctx, keepEvents)
		if err != nil {
			return NewSystemError(err, "reset failed")
		}
		printResetSummary(resetCounts{
			Claims:        gc.Claims,
			Evidence:      gc.Evidence,
			StatusHistory: gc.StatusHistory,
			Relationships: gc.Relationships,
			Embeddings:    gc.Embeddings,
			Events:        gc.Events,
		}, keepEvents)
		return nil
	})
	exitWithMnemosError(f.Verbose, err)
}

func handleDeleteClaim(args []string, f Flags) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: delete-claim requires at least one claim id")
		fmt.Fprintln(os.Stderr, "  mnemos delete-claim <id> [<id>...]")
		os.Exit(int(ExitUsage))
	}

	err := runJob("delete-claim", map[string]string{"ids": strings.Join(args, ",")}, f.Verbose, func(ctx context.Context, _ *workflow.Job, w *govwrite.Writer) error {
		var deletedClaims int64
		// Each claim's full cascade (relationships, embedding,
		// claim_evidence, claim_status_history, claim row) routes
		// through ONE governed action so the destructive op is a single
		// auditable entry on the evidence chain. Cross-claim atomicity is
		// best-effort; a partial failure leaves the store in a
		// recoverable state and surfaces via `mnemos doctor`.
		for _, id := range args {
			if err := w.DeleteClaimCascade(ctx, id); err != nil {
				return NewSystemError(err, "delete claim %s", id)
			}
			deletedClaims++
		}
		fmt.Printf("Deleted %d claim(s) and their evidence/embeddings/relationships.\n", deletedClaims)
		return nil
	})
	exitWithMnemosError(f.Verbose, err)
}

func handleDeleteEvent(args []string, f Flags) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: delete-event requires at least one event id")
		fmt.Fprintln(os.Stderr, "  mnemos delete-event <id> [<id>...]")
		os.Exit(int(ExitUsage))
	}

	err := runJob("delete-event", map[string]string{"ids": strings.Join(args, ",")}, f.Verbose, func(ctx context.Context, _ *workflow.Job, w *govwrite.Writer) error {
		var deletedEvents, cascadedClaims int64
		for _, id := range args {
			// One governed action cascades the event delete through its
			// dependent claims (relationships, embedding, claim cascade)
			// and then drops the event embedding + event row — a single
			// auditable entry per event on the evidence chain.
			n, err := w.DeleteEventCascade(ctx, id)
			if err != nil {
				return NewSystemError(err, "delete event %s", id)
			}
			cascadedClaims += int64(n)
			deletedEvents++
		}
		fmt.Printf("Deleted %d event(s); cascaded %d claim(s).\n", deletedEvents, cascadedClaims)
		return nil
	})
	exitWithMnemosError(f.Verbose, err)
}

// handleDedupe runs the semantic-dedupe pipeline against the local
// claim store. Defaults to dry-run because the operation is
// destructive (claims are merged, others deleted); --apply commits.
//
// Threshold default 0.92 is conservative on purpose. Lowering to
// 0.85 catches more paraphrases but also more legitimate distinct
// claims. Users should re-tune for their corpus.
func handleDedupe(args []string, f Flags) {
	threshold := 0.92
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--threshold":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--threshold requires a value in (0, 1]"))
				return
			}
			t, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil || t <= 0 || t > 1 {
				exitWithMnemosError(false, NewUserError("--threshold must be a float in (0, 1]"))
				return
			}
			threshold = t
			i++
		default:
			exitWithMnemosError(false, NewUserError("unknown argument %q for dedup", args[i]))
			return
		}
	}

	// --apply must be opt-in; default is dry-run. We borrow Flags.Force
	// for "yes really apply this" so users get a single mental model
	// across reembed, dedupe, etc.
	apply := f.Force
	if !apply && !f.DryRun {
		// Neither flag set → still default to dry-run, just say so.
		f.DryRun = true
	}

	err := runJob("dedup", map[string]string{
		"threshold": strconv.FormatFloat(threshold, 'f', 2, 64),
		"apply":     fmt.Sprintf("%t", apply),
	}, f.Verbose, func(ctx context.Context, _ *workflow.Job, w *govwrite.Writer) error {
		conn := w.Conn()
		plan, err := pipeline.PlanSemanticDedupe(ctx, conn, threshold)
		if err != nil {
			return NewSystemError(err, "plan semantic dedupe")
		}
		printDedupePlan(plan)
		if !apply {
			fmt.Println("\nDry run. Re-run with --force to apply.")
			return nil
		}
		merged, err := pipeline.ApplySemanticDedupe(ctx, conn, plan)
		if err != nil {
			return NewSystemError(err, "apply semantic dedupe")
		}
		fmt.Printf("\nMerged %d duplicate claim(s).\n", merged)
		// Trust ranking depends on the evidence count we just
		// changed; recompute so the next query sees fresh scores.
		now := time.Now().UTC()
		if scorer, ok := conn.Claims.(ports.TrustScorer); ok {
			if _, err := scorer.RecomputeTrust(ctx, func(confidence float64, evidenceCount int, latestEvidence time.Time) float64 {
				return trust.Score(confidence, evidenceCount, latestEvidence, now)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: post-dedupe trust recompute failed: %v\n", err)
			}
		}
		return nil
	})
	exitWithMnemosError(f.Verbose, err)
}

func printDedupePlan(plan pipeline.SemanticDedupePlan) {
	fmt.Printf("Semantic dedupe plan (threshold=%.2f)\n", plan.Threshold)
	fmt.Printf("  scanned:   %d claim(s) with embeddings\n", plan.ClaimsScanned)
	if plan.SkippedNoEmbedding > 0 {
		fmt.Printf("  skipped:   %d claim(s) without embeddings (run 'mnemos reembed' to include them)\n", plan.SkippedNoEmbedding)
	}
	if len(plan.Merges) == 0 {
		fmt.Println("  no near-duplicates found.")
		return
	}
	fmt.Printf("  proposing: %d merge(s)\n", len(plan.Merges))
	for i, m := range plan.Merges {
		fmt.Printf("    %d. winner=%s sim=%.3f absorbs %d duplicate(s): %s\n",
			i+1, m.WinnerID, m.MaxSimilarity, len(m.DuplicateIDs), strings.Join(m.DuplicateIDs, ", "))
	}
}

// handleRecomputeTrust rebuilds trust_score for every claim under the
// current scoring policy. Useful after upgrading (the v1→v2 migration
// adds the column with default 0; this command actually populates
// it), after tuning the trust constants in internal/trust, or as a
// nightly cron via `mnemos schedule`.
func handleRecomputeTrust(args []string, f Flags) {
	for _, a := range args {
		if a != "--all" {
			fmt.Fprintf(os.Stderr, "error: unknown argument %q for recompute-trust\n", a)
			fmt.Fprintln(os.Stderr, "  mnemos recompute-trust [--all]")
			os.Exit(int(ExitUsage))
		}
	}

	err := runJob("recompute-trust", map[string]string{}, f.Verbose, func(ctx context.Context, _ *workflow.Job, w *govwrite.Writer) error {
		conn := w.Conn()
		scorer, ok := conn.Claims.(ports.TrustScorer)
		if !ok {
			return NewSystemError(fmt.Errorf("backend %T does not support trust scoring", conn.Claims), "recompute trust")
		}
		now := time.Now().UTC()
		n, err := scorer.RecomputeTrust(ctx, func(confidence float64, evidenceCount int, latestEvidence time.Time) float64 {
			return trust.Score(confidence, evidenceCount, latestEvidence, now)
		})
		if err != nil {
			return NewSystemError(err, "recompute trust")
		}
		fmt.Printf("Recomputed trust for %d claim(s).\n", n)
		return nil
	})
	exitWithMnemosError(f.Verbose, err)
}

func handleReembed(args []string, f Flags) {
	for _, a := range args {
		switch a {
		default:
			fmt.Fprintf(os.Stderr, "error: unknown argument %q for reembed\n", a)
			fmt.Fprintln(os.Stderr, "  mnemos reembed [--force] [--dry-run]")
			os.Exit(int(ExitUsage))
		}
	}

	err := runJob("reembed", map[string]string{"force": fmt.Sprintf("%t", f.Force), "dry_run": fmt.Sprintf("%t", f.DryRun)}, f.Verbose, func(ctx context.Context, _ *workflow.Job, w *govwrite.Writer) error {
		conn := w.Conn()
		// Determine which claim ids need (re-)embedding through
		// ports — Claims.ListAll for --force, the dedicated
		// ListIDsMissingEmbedding anti-join otherwise.
		var ids []string
		allClaims, err := conn.Claims.ListAll(ctx)
		if err != nil {
			return NewSystemError(err, "list claims")
		}
		if f.Force {
			ids = make([]string, 0, len(allClaims))
			for _, c := range allClaims {
				ids = append(ids, c.ID)
			}
		} else {
			missing, err := conn.Claims.ListIDsMissingEmbedding(ctx)
			if err != nil {
				return NewSystemError(err, "list missing embeddings")
			}
			ids = missing
		}

		if len(ids) == 0 {
			fmt.Println("No claims need embeddings. Nothing to do.")
			return nil
		}

		if f.DryRun {
			fmt.Printf("Would (re)embed %d claim(s). Run without --dry-run to apply.\n", len(ids))
			return nil
		}

		text := make(map[string]string, len(allClaims))
		for _, c := range allClaims {
			text[c.ID] = c.Text
		}

		cfg, err := embedding.ConfigFromEnv()
		if err != nil {
			return NewSystemError(err, "embedding config")
		}
		client, err := embedding.NewClient(cfg)
		if err != nil {
			return NewSystemError(err, "embedding client")
		}

		texts := make([]string, 0, len(ids))
		keep := make([]string, 0, len(ids))
		for _, id := range ids {
			t, ok := text[id]
			if !ok {
				continue
			}
			texts = append(texts, t)
			keep = append(keep, id)
		}

		vectors, err := client.Embed(ctx, texts)
		if err != nil {
			return NewSystemError(err, "embed claims")
		}

		for i, id := range keep {
			if i >= len(vectors) {
				break
			}
			if err := w.Embedding(ctx, id, "claim", vectors[i], cfg.Model, ""); err != nil {
				return NewSystemError(err, "store embedding for %s", id)
			}
		}

		fmt.Printf("Embedded %d claim(s) with %s/%s.\n", len(vectors), cfg.Provider, cfg.Model)
		return nil
	})
	exitWithMnemosError(f.Verbose, err)
}

func printResetSummary(c resetCounts, keepEvents bool) {
	fmt.Printf("Reset complete (db=%s)\n", resolveDBPath())
	fmt.Printf("  claims:        %-8d (deleted)\n", c.Claims)
	fmt.Printf("  evidence:      %-8d (deleted)\n", c.Evidence)
	fmt.Printf("  status hist:   %-8d (deleted)\n", c.StatusHistory)
	fmt.Printf("  relationships: %-8d (deleted)\n", c.Relationships)
	fmt.Printf("  embeddings:    %-8d (deleted)\n", c.Embeddings)
	if keepEvents {
		fmt.Printf("  events:        kept\n")
	} else {
		fmt.Printf("  events:        %-8d (deleted)\n", c.Events)
	}
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}
