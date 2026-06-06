// Package synthesize derives Lessons from Action -> Outcome chains.
//
// The engine clusters actions by (subject, kind), pulls every Outcome
// that points back to a clustered Action, and emits a Lesson when the
// cluster carries enough corroborating evidence with consistent
// outcomes. Confidence is a product of three signals: corroboration
// count, outcome consistency, and recency. The synthesis trigger is
// hybrid by design: cheap incremental clustering on every write, plus
// a periodic full re-cluster that re-evaluates every cluster from
// scratch (the "sleep" pass in brain-best-practice memory
// consolidation). Manual `mnemos synthesize` invokes the same code
// path so operators can force a re-derivation on demand.
package synthesize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// Options tunes the synthesis pass without forcing every call site to
// pass a long argument list. Zero-valued Options reproduce the
// project-default thresholds.
type Options struct {
	// MinCorroboration is the smallest cluster size that may emit a
	// lesson. Defaults to domain.LessonMinCorroboration (3).
	MinCorroboration int
	// MinConfidence is the floor below which candidates are dropped.
	// Defaults to domain.LessonConfidenceMin (0.55).
	MinConfidence float64
	// HalfLifeDays controls how strongly recency weights the
	// confidence formula. A cluster's youngest action is treated as
	// "now"; older actions decay toward zero with this half-life.
	// Defaults to 90 days, matching internal/trust.
	HalfLifeDays float64
	// Now overrides the wall clock — useful for deterministic tests.
	Now func() time.Time
	// Source labels emitted lessons. Defaults to "synthesize".
	Source string
	// CreatedBy stamps lessons. Defaults to domain.SystemUser.
	CreatedBy string
}

func (o Options) withDefaults() Options {
	if o.MinCorroboration == 0 {
		o.MinCorroboration = domain.LessonMinCorroboration
	}
	if o.MinConfidence == 0 {
		o.MinConfidence = domain.LessonConfidenceMin
	}
	if o.HalfLifeDays == 0 {
		o.HalfLifeDays = 90
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.Source == "" {
		o.Source = "synthesize"
	}
	if o.CreatedBy == "" {
		o.CreatedBy = domain.SystemUser
	}
	return o
}

// Result reports how a synthesis pass ran. Counts are useful both as
// a CLI summary and as a regression signal in tests.
type Result struct {
	Clusters       int
	LessonsEmitted int
	Skipped        int      // dropped below MinCorroboration or MinConfidence
	LessonIDs      []string // ids of every lesson emitted (or refreshed)
}

// Synthesize runs one full pass over the action and outcome stores
// and writes derived lessons to the lesson store. Idempotent: re-
// running on the same data refreshes confidence and last_verified
// without churning lesson identity. The cluster id is a deterministic
// hash of (scope, kind), so a given cluster always points at the
// same lesson row.
func Synthesize(ctx context.Context, actions ports.ActionRepository, outcomes ports.OutcomeRepository, lessons ports.LessonRepository, opts Options) (Result, error) {
	opts = opts.withDefaults()

	allActions, err := actions.ListAll(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("list actions: %w", err)
	}
	allOutcomes, err := outcomes.ListAll(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("list outcomes: %w", err)
	}

	outcomesByAction := map[string][]domain.Outcome{}
	for _, o := range allOutcomes {
		outcomesByAction[o.ActionID] = append(outcomesByAction[o.ActionID], o)
	}

	clusters := clusterActions(allActions, outcomesByAction)

	res := Result{Clusters: len(clusters)}
	res.LessonIDs = make([]string, 0, len(clusters))
	now := opts.Now().UTC()

	for _, c := range clusters {
		if len(c.Actions) < opts.MinCorroboration {
			res.Skipped++
			continue
		}
		score, consistent := scoreCluster(c, opts.HalfLifeDays, now)
		if !consistent {
			res.Skipped++
			continue
		}
		if score < opts.MinConfidence {
			res.Skipped++
			continue
		}
		lesson := domain.Lesson{
			ID:           clusterID(c),
			Statement:    composeStatement(c),
			Scope:        c.Scope,
			Trigger:      c.Trigger,
			Kind:         c.Kind,
			Evidence:     actionIDs(c.Actions),
			Confidence:   score,
			Polarity:     clusterPolarity(c),
			DerivedAt:    now,
			LastVerified: now,
			Source:       opts.Source,
			CreatedBy:    opts.CreatedBy,
		}
		if err := lessons.Append(ctx, lesson); err != nil {
			return res, fmt.Errorf("append lesson %s: %w", lesson.ID, err)
		}
		res.LessonIDs = append(res.LessonIDs, lesson.ID)
		res.LessonsEmitted++
	}
	return res, nil
}

type cluster struct {
	Scope    domain.LessonScope
	Trigger  string
	Kind     string
	Actions  []domain.Action
	Outcomes [][]domain.Outcome // outcomes[i] are the outcomes for Actions[i]
}

// clusterActions groups actions that share the same operational
// surface (subject + kind). The subject becomes the lesson's
// Scope.Service and the kind labels both Lesson.Kind and Lesson.Trigger.
// Actions without any matching outcome are skipped — they cannot
// teach us anything about what happened next.
func clusterActions(actions []domain.Action, outcomesByAction map[string][]domain.Outcome) []cluster {
	type key struct {
		Service string
		Kind    string
	}
	buckets := map[key]*cluster{}
	for _, a := range actions {
		os, ok := outcomesByAction[a.ID]
		if !ok || len(os) == 0 {
			continue
		}
		k := key{Service: a.Subject, Kind: string(a.Kind)}
		c, exists := buckets[k]
		if !exists {
			c = &cluster{
				Scope:   domain.LessonScope{Service: a.Subject},
				Trigger: triggerLabel(string(a.Kind), a.Subject),
				Kind:    string(a.Kind),
			}
			buckets[k] = c
		}
		c.Actions = append(c.Actions, a)
		c.Outcomes = append(c.Outcomes, os)
	}

	out := make([]cluster, 0, len(buckets))
	for _, c := range buckets {
		// Stable cluster ordering — newest actions first inside a
		// cluster, sorted by action time. The synthesis decision is
		// invariant to ordering but tests deserve determinism.
		sort.Slice(c.Actions, func(i, j int) bool { return c.Actions[i].At.After(c.Actions[j].At) })
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope.Service != out[j].Scope.Service {
			return out[i].Scope.Service < out[j].Scope.Service
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// scoreCluster computes the confidence score for a cluster and
// reports whether the outcomes are consistent enough to emit a
// lesson at all. The score is a product:
//
//	confidence = corroboration × consistency × recency
//
// where:
//   - corroboration saturates at 6 actions: 1 - exp(-N/3) so a
//     3-action cluster scores ~0.63 and a 6-action cluster ~0.86.
//   - consistency is the fraction of outcomes with the cluster's
//     dominant Result. Below 2/3 we return consistent=false so
//     contradictory clusters never emit a folkloric lesson.
//   - recency is exp(-age_in_days * ln(2) / half_life). Newest
//     action in the cluster anchors the age — an old cluster decays
//     even if it once carried strong corroboration, encouraging the
//     synthesis layer to favour fresh evidence.
func scoreCluster(c cluster, halfLifeDays float64, now time.Time) (float64, bool) {
	if len(c.Actions) == 0 {
		return 0, false
	}
	flatOutcomes := flattenOutcomes(c)
	dominantResult, dominantCount := dominantResult(flatOutcomes)
	if dominantResult == "" {
		return 0, false
	}
	consistency := float64(dominantCount) / float64(len(flatOutcomes))
	if consistency < 2.0/3.0 {
		return 0, false
	}

	corroboration := 1 - math.Exp(-float64(len(c.Actions))/3.0)

	youngest := c.Actions[0].At
	for _, a := range c.Actions[1:] {
		if a.At.After(youngest) {
			youngest = a.At
		}
	}
	ageDays := now.Sub(youngest).Hours() / 24.0
	if ageDays < 0 {
		ageDays = 0
	}
	recency := math.Exp(-ageDays * math.Ln2 / halfLifeDays)

	return corroboration * consistency * recency, true
}

func flattenOutcomes(c cluster) []domain.Outcome {
	total := 0
	for _, os := range c.Outcomes {
		total += len(os)
	}
	out := make([]domain.Outcome, 0, total)
	for _, os := range c.Outcomes {
		out = append(out, os...)
	}
	return out
}

func dominantResult(os []domain.Outcome) (domain.OutcomeResult, int) {
	counts := map[domain.OutcomeResult]int{}
	for _, o := range os {
		counts[o.Result]++
	}
	var best domain.OutcomeResult
	bestN := 0
	for r, n := range counts {
		if n > bestN {
			best = r
			bestN = n
		}
	}
	return best, bestN
}

func triggerLabel(kind, subject string) string {
	if kind == "" || subject == "" {
		return ""
	}
	return fmt.Sprintf("%s_on_%s", strings.ReplaceAll(kind, " ", "_"), strings.ReplaceAll(subject, " ", "_"))
}

// composeStatement renders a short, human-readable summary describing
// the cluster's dominant pattern. Synthesis treats the statement as a
// hint, not a contract — playbook authors and operators can override
// it via the lesson's Source="human" path.
func composeStatement(c cluster) string {
	flat := flattenOutcomes(c)
	dom, _ := dominantResult(flat)
	switch dom {
	case domain.OutcomeResultSuccess:
		return fmt.Sprintf("%s on %s reliably succeeds in this corpus", c.Kind, c.Scope.Service)
	case domain.OutcomeResultFailure:
		return fmt.Sprintf("ANTI-LESSON: %s on %s reliably fails — do not retry without a fix", c.Kind, c.Scope.Service)
	case domain.OutcomeResultPartial:
		return fmt.Sprintf("%s on %s tends to land partial outcomes — expect manual cleanup", c.Kind, c.Scope.Service)
	default:
		return fmt.Sprintf("%s on %s has been observed but the outcome is unclear", c.Kind, c.Scope.Service)
	}
}

// clusterPolarity returns the polarity label for a cluster based on
// its dominant outcome result.
func clusterPolarity(c cluster) domain.LessonPolarity {
	flat := flattenOutcomes(c)
	dom, _ := dominantResult(flat)
	if dom == domain.OutcomeResultFailure {
		return domain.LessonPolarityNegative
	}
	return domain.LessonPolarityPositive
}

func actionIDs(as []domain.Action) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.ID)
	}
	return out
}

// clusterID is a deterministic stable id for a (scope, kind) cluster.
// Re-running synthesis returns the same id so the LessonRepository
// upsert path can ratchet confidence without churning the row.
func clusterID(c cluster) string {
	h := sha256.New()
	_, _ = h.Write([]byte(c.Scope.Service))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(c.Scope.Env))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(c.Scope.Team))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(c.Kind))
	return "ls_" + hex.EncodeToString(h.Sum(nil)[:8])
}
