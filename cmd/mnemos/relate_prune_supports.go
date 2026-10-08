package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/relate"
	"go.klarlabs.de/mnemos/internal/workflow"
)

// `mnemos relate --prune-supports [--top-k N] [--apply]` applies the supports
// budget (ADR 0027) to edges a brain already holds.
//
// New writes keep at most DefaultSupportsBudget supports edges per claim, but
// brains written before that hold every edge the old rule produced: a real
// 233k-claim brain had 32.4M of them. Nothing thins them on upgrade, on
// purpose. Deleting tens of millions of rows is the operator's decision, so
// this command reports by default and deletes only with --apply.
//
// It works one claim at a time (ListByClaim, then DeleteByIDs in batches), so
// memory stays proportional to the claim count, not the edge count. Only
// supports edges are touched, ranked exactly as the write path ranks them.
// Contradictions and every other type stay.

// pruneSupportsBatch is how many edge ids accumulate before one delete.
const pruneSupportsBatch = 5000

type pruneSupportsOpts struct {
	topK  int
	apply bool
}

func parsePruneSupportsArgs(args []string, f Flags) (pruneSupportsOpts, error) {
	o := pruneSupportsOpts{topK: relate.DefaultSupportsBudget}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--apply":
			o.apply = true
		case "--top-k":
			if i+1 >= len(args) {
				return o, NewUserError("--top-k needs a value")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 {
				return o, NewUserError("--top-k must be a positive integer (got %q)", args[i+1])
			}
			o.topK = n
			i++
		default:
			return o, NewUserError("relate --prune-supports: unexpected argument %q", a)
		}
	}
	// The global --dry-run wins over --apply: asking for both is asking not
	// to write.
	if f.DryRun {
		o.apply = false
	}
	return o, nil
}

type pruneSupportsReport struct {
	claims, overBudget int
	dropped, deleted   int64
}

func pruneSupports(args []string, f Flags) {
	o, err := parsePruneSupportsArgs(args, f)
	if err != nil {
		exitWithMnemosError(f.Verbose, err)
		return
	}
	meta := map[string]string{"top_k": strconv.Itoa(o.topK), "apply": strconv.FormatBool(o.apply)}
	err = runJobWithin(maintenanceJobTimeout(), "relate-prune-supports", meta, f.Verbose, func(ctx context.Context, job *workflow.Job, w *govwrite.Writer) error {
		rep, err := runPruneSupports(ctx, w.Conn().Claims, w.Conn().Relationships, o, func(r pruneSupportsReport) {
			fmt.Fprintf(os.Stderr, "  %d claims scanned, %d over budget, %d edges %s\n",
				r.claims, r.overBudget, r.dropped, map[bool]string{true: "deleted", false: "to remove"}[o.apply])
		})
		if err != nil {
			return err
		}
		fmt.Printf("claims scanned:       %d\n", rep.claims)
		fmt.Printf("claims over budget:   %d (more than %d supports edges)\n", rep.overBudget, o.topK)
		if o.apply {
			fmt.Printf("supports edges removed: %d\n", rep.deleted)
		} else {
			fmt.Printf("supports edges to remove: %d\n", rep.dropped)
			fmt.Println("dry run: nothing was deleted. Re-run with --apply to remove them.")
		}
		return nil
	})
	if err != nil {
		exitWithMnemosError(f.Verbose, err)
	}
}

// runPruneSupports is the command's core, separated from the job wrapper so
// it can be tested against a real store.
func runPruneSupports(ctx context.Context, claimsRepo ports.ClaimRepository, rels ports.RelationshipRepository,
	o pruneSupportsOpts, progress func(pruneSupportsReport)) (pruneSupportsReport, error) {
	var rep pruneSupportsReport
	var deleter ports.RelationshipDeleter
	if o.apply {
		d, ok := rels.(ports.RelationshipDeleter)
		if !ok {
			return rep, NewUserError("this storage backend cannot delete relationships by id")
		}
		deleter = d
	}
	claims, err := claimsRepo.ListAll(ctx)
	if err != nil {
		return rep, NewSystemError(err, "load claims")
	}
	pruner := relate.NewSupportsPruner(claims, o.topK)

	var pending []string
	flush := func() error {
		if !o.apply || len(pending) == 0 {
			pending = pending[:0]
			return nil
		}
		n, err := deleter.DeleteByIDs(ctx, pending)
		if err != nil {
			return NewSystemError(err, "delete supports edges")
		}
		rep.deleted += n
		pending = pending[:0]
		return nil
	}
	last := time.Now()
	for _, c := range claims {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		edges, err := rels.ListByClaim(ctx, c.ID)
		if err != nil {
			return rep, NewSystemError(err, "list relationships for %s", c.ID)
		}
		rep.claims++
		if drop := pruner.OverBudget(c.ID, edges); len(drop) > 0 {
			rep.overBudget++
			rep.dropped += int64(len(drop))
			pending = append(pending, drop...)
			if len(pending) >= pruneSupportsBatch {
				if err := flush(); err != nil {
					return rep, err
				}
			}
		}
		if progress != nil && time.Since(last) > 10*time.Second {
			progress(rep)
			last = time.Now()
		}
	}
	return rep, flush()
}
