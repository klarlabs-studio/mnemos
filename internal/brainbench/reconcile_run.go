package brainbench

import (
	"context"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/relate"
)

// NamedJudge is a PairJudge and the model it stands for.
type NamedJudge struct {
	Model string
	Judge PairJudge
}

// ReconcileOptions configures a run.
type ReconcileOptions struct {
	// Judges are the models to run the llm and hybrid arms with. Empty runs
	// the deterministic arm only; the other arms are then reported as not run,
	// never as zero.
	Judges []NamedJudge
	// BootstrapIters is the number of bootstrap resamples (default 2000).
	BootstrapIters int
	// Seed fixes the bootstrap resampling.
	Seed uint64
	// Progress, when set, is called after every LLM judgement.
	Progress func(model string, done, total int)
}

// PairOutcome is everything the run decided about one pair.
type PairOutcome struct {
	Pair          ReconcilePair        `json:"pair"`
	Deterministic DetVerdict           `json:"deterministic"`
	DetMS         float64              `json:"deterministic_ms"`
	Route         string               `json:"hybrid_route"`
	LLM           map[string]Judgement `json:"llm,omitempty"`
}

// Interval is a bootstrap percentile interval. Defined is how many resamples
// produced a defined value; an interval over few defined resamples is weak.
type Interval struct {
	Lo      float64 `json:"lo"`
	Hi      float64 `json:"hi"`
	Defined int     `json:"defined_resamples"`
}

// SplitMetrics scores one arm on one split. A metric whose denominator is zero
// is nil (unknown), never 0: a 0 would read as a measured failure.
type SplitMetrics struct {
	N           int       `json:"n"`
	Positives   int       `json:"labelled_contradicts"`
	TP          int       `json:"tp"`
	FP          int       `json:"fp"`
	FN          int       `json:"fn"`
	TN          int       `json:"tn"`
	Precision   *float64  `json:"precision"`
	Recall      *float64  `json:"recall"`
	F1          *float64  `json:"f1"`
	PrecisionCI *Interval `json:"precision_ci95,omitempty"`
	RecallCI    *Interval `json:"recall_ci95,omitempty"`
	F1CI        *Interval `json:"f1_ci95,omitempty"`
}

// ArmCost is what an arm spent over a set of pairs.
type ArmCost struct {
	Pairs           int     `json:"pairs"`
	DeterministicMS float64 `json:"deterministic_ms"`
	LLMCalls        int     `json:"llm_calls"`
	LLMWallMS       float64 `json:"llm_wall_ms"`
	LLMP50MS        float64 `json:"llm_p50_ms"`
	LLMP95MS        float64 `json:"llm_p95_ms"`
	// Tokens are as the provider reported them; TokensReported is false when
	// it reported none, so a missing count is never read as zero tokens.
	PromptTokens     int  `json:"prompt_tokens"`
	CompletionTokens int  `json:"completion_tokens"`
	TokensReported   bool `json:"tokens_reported"`
	CallErrors       int  `json:"call_errors"`
	Unparsed         int  `json:"unparsed_replies"`
}

// CategoryCount is how an arm did on one category of pairs (both splits).
type CategoryCount struct {
	Category string `json:"category"`
	Hand     bool   `json:"hand_written"`
	N        int    `json:"n"`
	Labelled int    `json:"labelled_contradicts"`
	Edges    int    `json:"predicted_contradicts"`
	Correct  int    `json:"correct"`
	LLMCalls int    `json:"llm_calls"`
}

// HardCaseEdges counts, over the hand-written pairs, how many each arm turned
// into contradiction edges — the downstream question: which blind-spot cases
// would an arm have linked, and how many compatible pairs would it wrongly
// have linked.
type HardCaseEdges struct {
	Contradicts      int `json:"labelled_contradicts"`
	ContradictsEdged int `json:"contradicts_turned_into_edges"`
	Compatible       int `json:"labelled_compatible"`
	CompatibleEdged  int `json:"compatible_turned_into_edges"`
}

// ArmResult is one arm (with one model, for the llm and hybrid arms).
type ArmResult struct {
	Arm   string       `json:"arm"`
	Model string       `json:"model,omitempty"`
	Test  SplitMetrics `json:"test"`
	Dev   SplitMetrics `json:"dev"`
	All   SplitMetrics `json:"all"`
	// DeltaF1 is this arm's test F1 minus the deterministic arm's, with a
	// paired bootstrap interval (both arms scored on the same resample).
	DeltaF1    *float64        `json:"delta_f1_vs_deterministic,omitempty"`
	DeltaF1CI  *Interval       `json:"delta_f1_ci95,omitempty"`
	CostTest   ArmCost         `json:"cost_test"`
	CostAll    ArmCost         `json:"cost_all"`
	Categories []CategoryCount `json:"categories"`
	HardCases  HardCaseEdges   `json:"hard_cases"`
	// RouteCounts (hybrid only) is how many pairs took each route.
	RouteCounts map[string]int `json:"route_counts,omitempty"`
}

// DatasetSummary describes the pair set.
type DatasetSummary struct {
	Total           int            `json:"total"`
	Contradicts     int            `json:"contradicts"`
	Compatible      int            `json:"compatible"`
	Dev             int            `json:"dev"`
	Test            int            `json:"test"`
	TestContradicts int            `json:"test_contradicts"`
	Hand            int            `json:"hand_written"`
	ByCategory      map[string]int `json:"by_category"`
	BySource        map[string]int `json:"by_source"`
}

// ReconcileReport is the experiment's output.
type ReconcileReport struct {
	Experiment         string         `json:"experiment"`
	Hypothesis         string         `json:"hypothesis"`
	Success            string         `json:"success_criterion"`
	PromptVersion      string         `json:"prompt_version"`
	Prompt             string         `json:"prompt"`
	RelateModelVersion string         `json:"relate_model_version"`
	RoutingRule        string         `json:"routing_rule"`
	StrengthThreshold  float64        `json:"strength_threshold"`
	BootstrapIters     int            `json:"bootstrap_iters"`
	Seed               uint64         `json:"seed"`
	Models             []string       `json:"models"`
	LLMRun             bool           `json:"llm_run"`
	Dataset            DatasetSummary `json:"dataset"`
	Arms               []ArmResult    `json:"arms"`
	// ThresholdSweep (dev split only) is descriptive: hybrid F1 on dev at other
	// thresholds. It did not choose the threshold and must not be used to.
	ThresholdSweep map[string][]SweepPoint `json:"dev_threshold_sweep,omitempty"`
	Pairs          []PairOutcome           `json:"pairs"`
	Limitations    []string                `json:"limitations"`
}

// SweepPoint is one threshold of the descriptive dev sweep.
type SweepPoint struct {
	Threshold float64  `json:"threshold"`
	F1        *float64 `json:"f1"`
	LLMCalls  int      `json:"llm_calls"`
}

// RunReconcile runs every arm over the set.
func RunReconcile(ctx context.Context, set ReconcileSet, opts ReconcileOptions) (ReconcileReport, error) {
	if opts.BootstrapIters <= 0 {
		opts.BootstrapIters = 2000
	}
	outcomes := make([]PairOutcome, len(set.Pairs))
	for i, p := range set.Pairs {
		start := time.Now()
		v, err := DeterministicVerdict(p.A, p.B)
		if err != nil {
			return ReconcileReport{}, err
		}
		outcomes[i] = PairOutcome{Pair: p, Deterministic: v, DetMS: ms(time.Since(start)), Route: HybridRoute(v)}
	}

	// One LLM pass per model over every pair. The hybrid arm draws its routed
	// pairs' judgements from this pass rather than asking again: the prompt
	// and sampling are identical, so a second call could only add sampling
	// noise, and sharing makes llm-vs-hybrid a paired comparison. The hybrid
	// arm's cost counts only the calls it routes.
	for _, j := range opts.Judges {
		for i := range outcomes {
			if err := ctx.Err(); err != nil {
				return ReconcileReport{}, err
			}
			if outcomes[i].LLM == nil {
				outcomes[i].LLM = map[string]Judgement{}
			}
			outcomes[i].LLM[j.Model] = j.Judge.Judge(ctx, outcomes[i].Pair.A, outcomes[i].Pair.B)
			if opts.Progress != nil {
				opts.Progress(j.Model, i+1, len(outcomes))
			}
		}
	}

	rep := ReconcileReport{
		Experiment:         "deterministic vs LLM reconciliation (ADR 0042)",
		Hypothesis:         "hybrid maintenance (deterministic where confident, LLM where not) reconciles beliefs more reliably than deterministic alone",
		Success:            "hybrid test F1 above deterministic with the paired 95% interval of the difference excluding 0, at an acceptable number of LLM calls, without an unacceptable recall loss",
		PromptVersion:      ReconcilePromptVersion,
		Prompt:             ReconcileSystemPrompt,
		RelateModelVersion: relate.ModelVersion,
		RoutingRule:        HybridRoutingRule,
		StrengthThreshold:  HybridStrengthThreshold,
		BootstrapIters:     opts.BootstrapIters,
		Seed:               opts.Seed,
		LLMRun:             len(opts.Judges) > 0,
		Dataset:            summarise(set),
		Pairs:              outcomes,
		Limitations:        ReconcileLimitations(),
	}

	detPred := predictions(outcomes, ArmDeterministic, "", HybridStrengthThreshold)
	det := scoreArm(outcomes, ArmDeterministic, "", detPred, nil, opts)
	rep.Arms = append(rep.Arms, det)
	rep.ThresholdSweep = map[string][]SweepPoint{}
	for _, j := range opts.Judges {
		rep.Models = append(rep.Models, j.Model)
		for _, arm := range []string{ArmLLM, ArmHybrid} {
			pred := predictions(outcomes, arm, j.Model, HybridStrengthThreshold)
			rep.Arms = append(rep.Arms, scoreArm(outcomes, arm, j.Model, pred, detPred, opts))
		}
		rep.ThresholdSweep[j.Model] = devSweep(outcomes, j.Model)
	}
	if len(rep.ThresholdSweep) == 0 {
		rep.ThresholdSweep = nil
	}
	return rep, nil
}

// predicted is one arm's decision about one pair.
type predicted struct {
	contradicts bool
	llmCalled   bool
}

// predictions applies an arm to every outcome. threshold is the hybrid
// routing threshold (HybridStrengthThreshold except in the dev sweep).
func predictions(outs []PairOutcome, arm, model string, threshold float64) []predicted {
	pred := make([]predicted, len(outs))
	for i, o := range outs {
		switch arm {
		case ArmDeterministic:
			pred[i] = predicted{contradicts: o.Deterministic.Contradicts}
		case ArmLLM:
			pred[i] = predicted{contradicts: o.LLM[model].Verdict == LabelContradicts, llmCalled: true}
		case ArmHybrid:
			route := routeAt(o.Deterministic, threshold)
			if RoutesToLLM(route) {
				pred[i] = predicted{contradicts: o.LLM[model].Verdict == LabelContradicts, llmCalled: true}
			} else {
				pred[i] = predicted{contradicts: route == RouteDeterministicEdge}
			}
		}
	}
	return pred
}

// routeAt is HybridRoute at an arbitrary threshold (for the dev sweep).
func routeAt(v DetVerdict, threshold float64) string {
	switch {
	case v.Contradicts && v.Strength >= threshold:
		return RouteDeterministicEdge
	case v.Contradicts:
		return RouteLowStrength
	case v.SharesTopic:
		return RouteTopicalNoEdge
	default:
		return RouteNoCandidate
	}
}

func scoreArm(outs []PairOutcome, arm, model string, pred, detPred []predicted, opts ReconcileOptions) ArmResult {
	res := ArmResult{Arm: arm, Model: model}
	var testIdx, devIdx, allIdx []int
	for i, o := range outs {
		allIdx = append(allIdx, i)
		if o.Pair.Split == SplitTest {
			testIdx = append(testIdx, i)
		} else {
			devIdx = append(devIdx, i)
		}
	}
	labels := make([]bool, len(outs))
	for i, o := range outs {
		labels[i] = o.Pair.Label == LabelContradicts
	}
	res.Test = scoreSplit(testIdx, labels, pred, opts, 1)
	res.Dev = scoreSplit(devIdx, labels, pred, opts, 2)
	res.All = scoreSplit(allIdx, labels, pred, opts, 3)
	if detPred != nil {
		res.DeltaF1, res.DeltaF1CI = pairedDeltaF1(testIdx, labels, pred, detPred, opts)
	}
	res.CostTest = armCost(outs, testIdx, arm, model, pred)
	res.CostAll = armCost(outs, allIdx, arm, model, pred)
	res.Categories = categoryCounts(outs, labels, pred)
	for i, o := range outs {
		if !o.Pair.Hand() {
			continue
		}
		if labels[i] {
			res.HardCases.Contradicts++
			if pred[i].contradicts {
				res.HardCases.ContradictsEdged++
			}
		} else {
			res.HardCases.Compatible++
			if pred[i].contradicts {
				res.HardCases.CompatibleEdged++
			}
		}
	}
	if arm == ArmHybrid {
		res.RouteCounts = map[string]int{}
		for _, o := range outs {
			res.RouteCounts[o.Route]++
		}
	}
	return res
}

// confusion counts TP/FP/FN/TN over idx.
func confusion(idx []int, labels []bool, pred []predicted) (tp, fp, fn, tn int) {
	for _, i := range idx {
		switch {
		case labels[i] && pred[i].contradicts:
			tp++
		case !labels[i] && pred[i].contradicts:
			fp++
		case labels[i]:
			fn++
		default:
			tn++
		}
	}
	return
}

// prf returns precision, recall and F1, each nil when its denominator is 0.
func prf(tp, fp, fn int) (p, r, f *float64) {
	if tp+fp > 0 {
		v := float64(tp) / float64(tp+fp)
		p = &v
	}
	if tp+fn > 0 {
		v := float64(tp) / float64(tp+fn)
		r = &v
	}
	if 2*tp+fp+fn > 0 {
		v := 2 * float64(tp) / float64(2*tp+fp+fn)
		f = &v
	}
	return
}

func scoreSplit(idx []int, labels []bool, pred []predicted, opts ReconcileOptions, stream uint64) SplitMetrics {
	tp, fp, fn, tn := confusion(idx, labels, pred)
	m := SplitMetrics{N: len(idx), TP: tp, FP: fp, FN: fn, TN: tn, Positives: tp + fn}
	m.Precision, m.Recall, m.F1 = prf(tp, fp, fn)
	if len(idx) == 0 {
		return m
	}
	rng := rand.New(rand.NewPCG(opts.Seed, stream))
	var ps, rs, fs []float64
	sample := make([]int, len(idx))
	for range opts.BootstrapIters {
		for k := range sample {
			sample[k] = idx[rng.IntN(len(idx))]
		}
		btp, bfp, bfn, _ := confusion(sample, labels, pred)
		p, r, f := prf(btp, bfp, bfn)
		if p != nil {
			ps = append(ps, *p)
		}
		if r != nil {
			rs = append(rs, *r)
		}
		if f != nil {
			fs = append(fs, *f)
		}
	}
	m.PrecisionCI, m.RecallCI, m.F1CI = percentile(ps), percentile(rs), percentile(fs)
	return m
}

// pairedDeltaF1 is arm F1 minus deterministic F1 on the test split, with a
// paired bootstrap: each resample scores both arms on the same pairs.
func pairedDeltaF1(idx []int, labels []bool, pred, detPred []predicted, opts ReconcileOptions) (*float64, *Interval) {
	tp, fp, fn, _ := confusion(idx, labels, pred)
	dtp, dfp, dfn, _ := confusion(idx, labels, detPred)
	_, _, f := prf(tp, fp, fn)
	_, _, df := prf(dtp, dfp, dfn)
	if f == nil || df == nil || len(idx) == 0 {
		return nil, nil
	}
	delta := *f - *df
	// The same stream for every arm, so every arm's delta is measured on the
	// same resamples.
	rng := rand.New(rand.NewPCG(opts.Seed, 99))
	var ds []float64
	sample := make([]int, len(idx))
	for range opts.BootstrapIters {
		for k := range sample {
			sample[k] = idx[rng.IntN(len(idx))]
		}
		a, b, c, _ := confusion(sample, labels, pred)
		x, y, z, _ := confusion(sample, labels, detPred)
		_, _, fa := prf(a, b, c)
		_, _, fd := prf(x, y, z)
		if fa != nil && fd != nil {
			ds = append(ds, *fa-*fd)
		}
	}
	return &delta, percentile(ds)
}

// percentile returns the 2.5th-97.5th percentile interval, or nil when no
// resample was defined.
func percentile(xs []float64) *Interval {
	if len(xs) == 0 {
		return nil
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	at := func(q float64) float64 {
		k := int(math.Floor(q * float64(len(s)-1)))
		return s[k]
	}
	return &Interval{Lo: at(0.025), Hi: at(0.975), Defined: len(s)}
}

func armCost(outs []PairOutcome, idx []int, arm, model string, pred []predicted) ArmCost {
	c := ArmCost{Pairs: len(idx)}
	var lats []float64
	for _, i := range idx {
		c.DeterministicMS += outs[i].DetMS
		if !pred[i].llmCalled {
			continue
		}
		j := outs[i].LLM[model]
		c.LLMCalls++
		lats = append(lats, j.LatencyMS)
		c.LLMWallMS += j.LatencyMS
		c.PromptTokens += j.InputTokens
		c.CompletionTokens += j.OutputTokens
		if j.InputTokens > 0 || j.OutputTokens > 0 {
			c.TokensReported = true
		}
		switch {
		case j.Err != "":
			c.CallErrors++
		case j.Verdict == "":
			c.Unparsed++
		}
	}
	if arm == ArmLLM {
		// The llm arm runs no deterministic pass.
		c.DeterministicMS = 0
	}
	sort.Float64s(lats)
	c.LLMP50MS, c.LLMP95MS = quantile(lats, 0.5), quantile(lats, 0.95)
	return c
}

// quantile is the nearest-rank quantile of sorted xs (0 when empty).
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	k := int(math.Ceil(q*float64(len(sorted)))) - 1
	return sorted[max(0, min(k, len(sorted)-1))]
}

func categoryCounts(outs []PairOutcome, labels []bool, pred []predicted) []CategoryCount {
	by := map[string]*CategoryCount{}
	var order []string
	for i, o := range outs {
		key := o.Pair.Category
		if o.Pair.Hand() {
			key += " (hand)"
		}
		cc, ok := by[key]
		if !ok {
			cc = &CategoryCount{Category: o.Pair.Category, Hand: o.Pair.Hand()}
			by[key] = cc
			order = append(order, key)
		}
		cc.N++
		if labels[i] {
			cc.Labelled++
		}
		if pred[i].contradicts {
			cc.Edges++
		}
		if pred[i].contradicts == labels[i] {
			cc.Correct++
		}
		if pred[i].llmCalled {
			cc.LLMCalls++
		}
	}
	sort.Strings(order)
	out := make([]CategoryCount, 0, len(order))
	for _, k := range order {
		out = append(out, *by[k])
	}
	return out
}

// devSweep is the descriptive dev-split sweep of the routing threshold.
func devSweep(outs []PairOutcome, model string) []SweepPoint {
	var devIdx []int
	labels := make([]bool, len(outs))
	for i, o := range outs {
		labels[i] = o.Pair.Label == LabelContradicts
		if o.Pair.Split == SplitDev {
			devIdx = append(devIdx, i)
		}
	}
	var pts []SweepPoint
	for _, th := range []float64{0, 0.5, 0.6, 0.65, 0.7, 0.8, 1.01} {
		pred := predictions(outs, ArmHybrid, model, th)
		tp, fp, fn, _ := confusion(devIdx, labels, pred)
		_, _, f := prf(tp, fp, fn)
		calls := 0
		for _, i := range devIdx {
			if pred[i].llmCalled {
				calls++
			}
		}
		pts = append(pts, SweepPoint{Threshold: th, F1: f, LLMCalls: calls})
	}
	return pts
}

func summarise(set ReconcileSet) DatasetSummary {
	s := DatasetSummary{ByCategory: map[string]int{}, BySource: map[string]int{}}
	for _, p := range set.Pairs {
		s.Total++
		if p.Label == LabelContradicts {
			s.Contradicts++
		} else {
			s.Compatible++
		}
		if p.Split == SplitDev {
			s.Dev++
		} else {
			s.Test++
			if p.Label == LabelContradicts {
				s.TestContradicts++
			}
		}
		if p.Hand() {
			s.Hand++
		}
		s.ByCategory[p.Category]++
		src := p.Source
		if k := strings.IndexByte(src, '#'); k >= 0 {
			src = src[:k]
		}
		s.BySource[src]++
	}
	return s
}

// ReconcileLimitations is what the experiment does not establish. It ships
// inside every report.
func ReconcileLimitations() []string {
	return []string{
		"Small, hand-labelled set: a few hundred pairs at most, so intervals are wide and a difference of a few pairs is within noise.",
		"The labeller is the team that wrote the detectors. Hand-written cases target the detectors' documented blind spots (ADR 0040), which biases the set against the deterministic arm on recall by design; corpus-derived pairs were mostly written to pass the detectors, which biases it the other way.",
		"Pairs are judged in isolation. Production relates a new claim against a candidate set and caps contradictions per claim; per-claim caps, session-noise suppression and candidate budgets do not act here.",
		"Labels are binary (contradicts vs compatible). Supersession is folded into contradicts; the direction (which side wins) is not scored.",
		"Local models only (Ollama), quantised; a cloud model may do better or worse. Latency is this machine's, sequential, after one untimed warm-up call.",
		"Temperature 0 and a fixed seed make a run repeatable on one machine, not across Ollama versions or hardware.",
		"The hybrid arm reuses the llm arm's judgement for each routed pair (same prompt, same sampling) rather than calling again; its cost counts only the routed calls.",
		"An unreadable or failed LLM reply counts as compatible (no edge), the fail-safe a production path would take; such replies are counted separately.",
		"One prompt version. Prompt wording was fixed before the run and not tuned; a tuned prompt could change the LLM numbers.",
	}
}
