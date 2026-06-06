// Package bias surfaces simple, explainable bias indicators on the
// evidence layer. Mnemos cannot detect every kind of bias —
// fairness, ideology, latent demographics — without much heavier
// infrastructure than belongs in an evidence layer. What it can do
// cheaply is flag structural patterns that often correlate with
// biased curation:
//
//   - Source concentration: one input or one author drives a
//     disproportionate share of the evidence.
//   - Polarity skew: claims of one stance dramatically outweigh
//     opposing claims on the same topic.
//   - Temporal clustering: evidence is concentrated in a narrow time
//     window, masking longer-running counter-evidence.
//   - Corroboration shape: too many claims rest on a single event
//     (single-source-of-truth pathology).
//
// Each indicator returns a `Finding` carrying a numeric score in
// `[0, 1]` plus a structured explanation. Operators decide what to
// do with the report — Mnemos does not auto-act on bias signals.
package bias

import (
	"math"
	"sort"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Finding is one signal in a Report. Score is a normalised
// severity in `[0, 1]`; Threshold is the operator-tunable level at
// or above which the finding is considered actionable.
type Finding struct {
	Kind        string  `json:"kind"`
	Score       float64 `json:"score"`
	Threshold   float64 `json:"threshold"`
	Explanation string  `json:"explanation"`
}

// Report collects every finding the analyser produced for an
// answer, query result, or claim cluster. An empty Findings slice
// means "no bias signals above the threshold".
type Report struct {
	Findings []Finding `json:"findings"`
}

// HasFindings is a convenience for the CLI / query summary line.
func (r Report) HasFindings() bool { return len(r.Findings) > 0 }

// AnalysisInput bundles the evidence the analyser needs. Pass the
// claims that participated in an answer plus the events those claims
// were derived from.
type AnalysisInput struct {
	Claims []domain.Claim
	Events []domain.Event
	// EvidenceMap maps claim_id → []event_id so the analyser can
	// reason about which claim leans on which event without re-
	// loading the bridge table.
	EvidenceMap map[string][]string
}

// Thresholds tunes when each indicator fires. Defaults are
// deliberately conservative — operators tighten as their corpus
// stabilises.
type Thresholds struct {
	SourceConcentration float64
	PolaritySkew        float64
	TemporalCluster     float64
	SingleSourceOfTruth float64
}

// DefaultThresholds is a starting point. Anything ≥ 0.6 typically
// warrants operator review; ≥ 0.85 is a strong signal.
func DefaultThresholds() Thresholds {
	return Thresholds{
		SourceConcentration: 0.6,
		PolaritySkew:        0.7,
		TemporalCluster:     0.7,
		SingleSourceOfTruth: 0.6,
	}
}

// Analyse runs every indicator against the input and returns a
// Report carrying findings whose score met the configured
// threshold.
func Analyse(in AnalysisInput, thr Thresholds) Report {
	out := Report{}
	if f := sourceConcentration(in, thr); f != nil {
		out.Findings = append(out.Findings, *f)
	}
	if f := polaritySkew(in, thr); f != nil {
		out.Findings = append(out.Findings, *f)
	}
	if f := temporalCluster(in, thr); f != nil {
		out.Findings = append(out.Findings, *f)
	}
	if f := singleSourceOfTruth(in, thr); f != nil {
		out.Findings = append(out.Findings, *f)
	}
	return out
}

// sourceConcentration returns a finding when one source_input_id
// drives ≥ threshold share of the evidence events. Score is the
// share of the dominant source.
func sourceConcentration(in AnalysisInput, thr Thresholds) *Finding {
	if len(in.Events) < 3 {
		return nil
	}
	counts := map[string]int{}
	for _, e := range in.Events {
		key := e.SourceInputID
		if key == "" {
			key = "<unknown>"
		}
		counts[key]++
	}
	var topKey string
	var top int
	for k, c := range counts {
		if c > top {
			topKey, top = k, c
		}
	}
	share := float64(top) / float64(len(in.Events))
	if share < thr.SourceConcentration {
		return nil
	}
	return &Finding{
		Kind:        "source_concentration",
		Score:       share,
		Threshold:   thr.SourceConcentration,
		Explanation: "single source " + topKey + " contributes a disproportionate share of evidence",
	}
}

// polaritySkew detects when claims with positive surface markers
// massively outnumber negative ones (or vice-versa). Heuristic only —
// negation isn't free in NLP. The point is to flag the lopsidedness;
// operators decide whether the corpus genuinely warrants it.
func polaritySkew(in AnalysisInput, thr Thresholds) *Finding {
	if len(in.Claims) < 4 {
		return nil
	}
	pos, neg := 0, 0
	for _, c := range in.Claims {
		switch claimPolarity(c.Text) {
		case +1:
			pos++
		case -1:
			neg++
		}
	}
	total := pos + neg
	if total < 4 {
		return nil
	}
	ratio := float64(absInt(pos-neg)) / float64(total)
	if ratio < thr.PolaritySkew {
		return nil
	}
	dom := "positive"
	if neg > pos {
		dom = "negative"
	}
	return &Finding{
		Kind:        "polarity_skew",
		Score:       ratio,
		Threshold:   thr.PolaritySkew,
		Explanation: dom + "-leaning claims dominate the answer set",
	}
}

// temporalCluster fires when evidence events fall within a narrow
// time window relative to the corpus span. Score is `1 - (window /
// span)` — perfectly co-located events score 1, evenly distributed
// score ~0.
func temporalCluster(in AnalysisInput, thr Thresholds) *Finding {
	if len(in.Events) < 4 {
		return nil
	}
	times := make([]time.Time, 0, len(in.Events))
	for _, e := range in.Events {
		if !e.Timestamp.IsZero() {
			times = append(times, e.Timestamp)
		}
	}
	if len(times) < 4 {
		return nil
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	span := times[len(times)-1].Sub(times[0])
	if span <= 0 {
		// All events at the same instant — extreme cluster.
		return &Finding{
			Kind: "temporal_cluster", Score: 1, Threshold: thr.TemporalCluster,
			Explanation: "evidence events share the same timestamp",
		}
	}
	// Window = densest 50% of events.
	half := len(times) / 2
	bestSpan := span
	for i := 0; i+half < len(times); i++ {
		w := times[i+half].Sub(times[i])
		if w < bestSpan {
			bestSpan = w
		}
	}
	score := 1 - float64(bestSpan)/float64(span)
	if math.IsNaN(score) || score < thr.TemporalCluster {
		return nil
	}
	return &Finding{
		Kind:        "temporal_cluster",
		Score:       score,
		Threshold:   thr.TemporalCluster,
		Explanation: "half of the evidence events are clustered into a narrow time window",
	}
}

// singleSourceOfTruth fires when a high share of claims rests on a
// single event. Even one piece of evidence per claim is fine; the
// pattern we flag is many claims all leaning on the same event,
// which masquerades as corroboration without actually adding it.
func singleSourceOfTruth(in AnalysisInput, thr Thresholds) *Finding {
	if len(in.Claims) < 3 || len(in.EvidenceMap) == 0 {
		return nil
	}
	eventClaimCount := map[string]int{}
	for _, eventIDs := range in.EvidenceMap {
		for _, eid := range eventIDs {
			eventClaimCount[eid]++
		}
	}
	var topEvent string
	var topCount int
	for eid, c := range eventClaimCount {
		if c > topCount {
			topEvent, topCount = eid, c
		}
	}
	share := float64(topCount) / float64(len(in.Claims))
	if share < thr.SingleSourceOfTruth {
		return nil
	}
	return &Finding{
		Kind:        "single_source_of_truth",
		Score:       share,
		Threshold:   thr.SingleSourceOfTruth,
		Explanation: "one event (" + topEvent + ") underlies a disproportionate share of the answer's claims",
	}
}

// claimPolarity returns +1 for affirmative claims, -1 for negative,
// 0 for neutral / unrecognised. Heuristic — token-level not
// linguistic. Words like "fail", "broken", "missed" land on the
// negative side; "shipped", "deployed", "succeeded" on the positive.
// Whitelisted to keep the false-positive rate manageable.
func claimPolarity(text string) int {
	t := strings.ToLower(text)
	pos := []string{"shipped", "deployed", "succeeded", "delivered", "passed", "completed", "approved"}
	neg := []string{"failed", "broken", "missed", "rejected", "cancelled", "regressed", "blocked", "outage"}
	for _, w := range neg {
		if strings.Contains(t, w) {
			return -1
		}
	}
	for _, w := range pos {
		if strings.Contains(t, w) {
			return +1
		}
	}
	return 0
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
