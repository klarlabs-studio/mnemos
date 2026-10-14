package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/narration"
	"go.klarlabs.de/mnemos/internal/workflow"
)

// `mnemos prune --narration --judge [--limit N] [--dry-run]` deprecates captured
// claims that the narration judge reads as the session narrating itself.
//
// The rule-based `prune --narration` catches shapes: lead-ins, headers, code
// lines. Most stored narration has a perfectly ordinary shape — "Both PRs are
// green", "Migration 43 is applied" — and only reading it tells it apart from
// knowledge. On a 41k-claim brain the rules would have caught 2.6% of what was
// stored, while a hand-labelled sample put narration near 73%.
//
// Only claims the capture hook wrote are reviewed (captureActor). A document,
// a deliberate record or a human edit is never second-guessed by this pass.
//
// Like the rule pass it only DEPRECATES, through the governed writer, so each
// transition is recorded in claim_status_history with the judge's model and
// prompt version, and can be reversed. It writes as it goes, one chunk per
// transaction, so it never holds the write lock for the length of the pass
// (#466), and a pass that runs out of time keeps what it already decided.
// Verdicts are cached, so a re-run resumes rather than starting over.
func pruneNarrationJudged(dryRun bool, limit int, f Flags) {
	err := runJob("prune-narration-judge", map[string]string{"dry_run": fmt.Sprint(dryRun), "limit": fmt.Sprint(limit)}, f.Verbose,
		func(ctx context.Context, job *workflow.Job, w *govwrite.Writer) error {
			conn := w.Conn()
			actor, actorErr := resolveActor(ctx, conn.Users, f.Actor)
			if actorErr != nil {
				return actorErr
			}
			judge, err := narrationJudgeFromEnv()
			if err != nil {
				return NewUserError("prune --narration --judge needs an LLM; set MNEMOS_LLM_PROVIDER (and MNEMOS_NARRATION_MODEL to pick the judge): %v", err)
			}
			if err := job.SetStatus("loading", ""); err != nil {
				return err
			}
			claims, err := conn.Claims.ListAll(ctx)
			if err != nil {
				return NewSystemError(err, "load claims")
			}
			candidates := selectJudgeCandidates(claims, limit)
			if err := job.SetStatus("extracting", ""); err != nil {
				return err
			}
			reason := govwrite.ClaimReason{
				Reason:    "Session narration, not knowledge (prune --narration --judge; judge " + judge.Identity() + ")",
				ChangedBy: actor,
			}
			write := func(ctx context.Context, batch []domain.Claim) error {
				_, err := w.Claims(ctx, batch, reason)
				return err
			}
			_, err = runNarrationJudgePass(ctx, judge, candidates, dryRun, write, os.Stdout)
			return err
		})
	if err != nil {
		exitWithMnemosError(f.Verbose, err)
	}
}

// narrationClassifier is the part of narration.Judge the pass uses.
type narrationClassifier interface {
	Classify(ctx context.Context, texts []string) ([]narration.Verdict, error)
	Identity() string
}

// narrationJudgeStats reports what one pass did.
type narrationJudgeStats struct {
	Candidates int
	Reviewed   int
	Dropped    int
	// Unanswered counts claims the judge gave no verdict for. They are kept,
	// and asked again on the next run.
	Unanswered int
	Written    int
	Partial    bool
}

// judgeChunk is how many claims are judged and written per transaction: ten
// judge requests, a few hundred milliseconds of write lock.
const judgeChunk = 10 * narration.BatchSize

// selectJudgeCandidates returns the active or contested claims the capture
// hook wrote, oldest first so a dry run and the apply after it walk the same
// order, capped at limit when limit > 0.
func selectJudgeCandidates(claims []domain.Claim, limit int) []domain.Claim {
	var out []domain.Claim
	for _, c := range claims {
		if isPrunableStatus(c.Status) && c.CreatedBy == captureActor {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// runNarrationJudgePass judges candidates chunk by chunk and, unless dryRun,
// deprecates each chunk's Drop verdicts through write before judging the next.
//
// A claim the rule-based filter already calls junk is dropped without asking
// the model. Every verdict other than Drop keeps the claim. Running out of time
// is a normal end for a pass this long: it reports how far it got and returns
// nil, and the chunks already written stay written.
func runNarrationJudgePass(
	ctx context.Context,
	judge narrationClassifier,
	candidates []domain.Claim,
	dryRun bool,
	write func(context.Context, []domain.Claim) error,
	out io.Writer,
) (narrationJudgeStats, error) {
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(out, format, args...) }
	stats := narrationJudgeStats{Candidates: len(candidates)}
	say("captured claims to review: %d (judge %s)\n", len(candidates), judge.Identity())

	judgeCtx, cancelJudge := withWriteReserve(ctx)
	defer cancelJudge()

	var sample []string
	for start := 0; start < len(candidates); start += judgeChunk {
		chunk := candidates[start:min(start+judgeChunk, len(candidates))]
		texts := make([]string, len(chunk))
		for i, c := range chunk {
			texts[i] = c.Text
		}
		verdicts, err := judge.Classify(judgeCtx, texts)
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return stats, NewSystemError(err, "judge narration")
		}

		var drop []domain.Claim
		for i, c := range chunk {
			if err == nil && verdicts[i] == narration.Unknown && !extract.IsJunk(c.Text) {
				stats.Unanswered++
			}
			if extract.IsJunk(c.Text) || verdicts[i] == narration.Drop {
				c.Status = domain.ClaimStatusDeprecated
				drop = append(drop, c)
				if len(sample) < 10 {
					sample = append(sample, c.Text)
				}
			}
		}
		stats.Dropped += len(drop)
		if err != nil {
			// Out of budget mid-chunk: the verdicts that did land are still
			// sound, so write them; the rest of the chunk stays as it is.
			stats.Partial = true
		} else {
			stats.Reviewed += len(chunk)
		}
		if !dryRun && len(drop) > 0 {
			if werr := write(ctx, drop); werr != nil {
				return stats, NewSystemError(werr, "deprecate narration claims")
			}
			stats.Written += len(drop)
		}
		say("  reviewed %d/%d, narration so far %d\n", min(start+len(chunk), len(candidates)), len(candidates), stats.Dropped)
		if stats.Partial {
			break
		}
	}

	say("\nnarration: %d of %d reviewed (%.1f%%)\n", stats.Dropped, stats.Reviewed, pct(stats.Dropped, stats.Reviewed))
	if stats.Unanswered > 0 {
		say("no verdict (kept, asked again next run): %d\n", stats.Unanswered)
	}
	for _, t := range sample {
		say("  - %s\n", truncateClaim(t))
	}
	if stats.Partial {
		say("\nstopped at the job budget after %d of %d; re-run to continue — verdicts are cached (raise MNEMOS_JOB_TIMEOUT to cover more per pass)\n",
			stats.Reviewed, stats.Candidates)
	}
	if dryRun {
		say("\n(dry run — nothing written; re-run without --dry-run to apply)\n")
		return stats, nil
	}
	say("\ndeprecated %d claim(s); each transition is recorded in claim_status_history and can be reversed.\n", stats.Written)
	return stats, nil
}
