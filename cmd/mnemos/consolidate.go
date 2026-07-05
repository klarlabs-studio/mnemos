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
//	                   [--reinforce-validated] [--reinforce-playbooks]
//	                   [--synthesize] [--replay-top-k <n>]
func handleConsolidate(args []string, _ Flags) {
	opts := mnemos.ConsolidateOptions{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			opts.DryRun = true
		case "--forget-refuted":
			opts.ForgetRefuted = true
		case "--reinforce-validated":
			opts.ReinforceValidated = true
		case "--reinforce-playbooks":
			opts.ReinforcePlaybooks = true
		case "--synthesize":
			opts.Synthesize = true
		case "--forget-below-trust":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--forget-below-trust requires a value"))
				return
			}
			f, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil || f < 0 || f > 1 {
				exitWithMnemosError(false, NewUserError("--forget-below-trust must be a float in [0, 1]"))
				return
			}
			opts.ForgetBelowTrust = f
			i++
		case "--replay-top-k":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--replay-top-k requires a value"))
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				exitWithMnemosError(false, NewUserError("--replay-top-k must be a non-negative integer"))
				return
			}
			opts.ReplayTopK = n
			i++
		default:
			exitWithMnemosError(false, NewUserError("unknown flag %q", args[i]))
			return
		}
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
		"replayed":              res.Replayed,
		"lessons_synthesized":   res.LessonsSynthesized,
		"playbooks_synthesized": res.PlaybooksSynthesized,
		"dry_run":               opts.DryRun,
	})
}
