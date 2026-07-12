package main

import (
	"context"
	"strconv"

	"go.klarlabs.de/mnemos"
)

// handleConsolidate routes `mnemos consolidate ...` — the cognitive "sleep" pass
// run as a one-shot CLI command, so an operator (or a scheduled Job) can maintain
// a Mnemos store the same way a library consumer calls Memory.Consolidate. It
// dedupes near-duplicate claims + refreshes trust always, and runs the optional
// stages (forget, reinforce, synthesize, replay) per flag. Deterministic, no LLM.
//
//	mnemos consolidate [--dry-run] [--forget-below-trust <f>] [--forget-refuted]
//	                   [--reinforce-validated] [--reinforce-playbooks] [--credit]
//	                   [--salience] [--synthesize] [--replay-top-k <n>]
func handleConsolidate(args []string, f Flags) {
	// The promotion pass (ADR 0011 Phase B) is a distinct, read-only sub-mode of
	// consolidate: tenant→global promotion rather than in-store maintenance.
	for _, a := range args {
		if a == "--promote" {
			handlePromote(args, f)
			return
		}
	}

	opts, err := parseConsolidateOpts(args, f)
	if err != nil {
		exitWithMnemosError(false, err)
		return
	}

	ctx := context.Background()
	mem, err := newLibraryMemory(ctx, "")
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer func() { _ = mem.Close() }()

	res, err := mem.Consolidate(ctx, opts)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "consolidate"))
		return
	}
	emitJSON(map[string]any{
		"claims_scanned":        res.ClaimsScanned,
		"duplicate_groups":      res.DuplicateGroups,
		"merged":                res.Merged,
		"trust_refreshed":       res.TrustRefreshed,
		"forgotten":             res.Forgotten,
		"refuted":               res.Refuted,
		"validated":             res.Validated,
		"playbooks_reinforced":  res.PlaybooksReinforced,
		"credited":              res.Credited,
		"salience_tagged":       res.SalienceTagged,
		"replayed":              res.Replayed,
		"lessons_synthesized":   res.LessonsSynthesized,
		"playbooks_synthesized": res.PlaybooksSynthesized,
		"dry_run":               opts.DryRun,
	})
}

// parseConsolidateOpts builds ConsolidateOptions from the sub-flags plus the
// global flags. DryRun comes from f (the global --dry-run stripped by
// ParseFlags), NOT from a local case — that was the bug where `consolidate
// --dry-run` still mutated the store. Split out so the wiring is testable.
func parseConsolidateOpts(args []string, f Flags) (mnemos.ConsolidateOptions, error) {
	opts := mnemos.ConsolidateOptions{DryRun: f.DryRun}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--forget-refuted":
			opts.ForgetRefuted = true
		case "--reinforce-validated":
			opts.ReinforceValidated = true
		case "--credit":
			opts.AssignCredit = true
		case "--salience":
			opts.AssignSalience = true
		case "--reinforce-playbooks":
			opts.ReinforcePlaybooks = true
		case "--synthesize":
			opts.Synthesize = true
		case "--forget-below-trust":
			if i+1 >= len(args) {
				return opts, NewUserError("--forget-below-trust requires a value")
			}
			val, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil || val < 0 || val > 1 {
				return opts, NewUserError("--forget-below-trust must be a float in [0, 1]")
			}
			opts.ForgetBelowTrust = val
			i++
		case "--replay-top-k":
			if i+1 >= len(args) {
				return opts, NewUserError("--replay-top-k requires a value")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				return opts, NewUserError("--replay-top-k must be a non-negative integer")
			}
			opts.ReplayTopK = n
			i++
		default:
			return opts, NewUserError("unknown flag %q", args[i])
		}
	}
	return opts, nil
}
