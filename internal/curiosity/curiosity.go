// Package curiosity turns Mnemos's passive metacognition (knowledge-gaps,
// calibration, trust/staleness) into an ACTIVE acquisition signal — the
// "what to learn / verify next" queue of ADR 0013 §3.
//
// Mnemos already knows what it does not know: unresolved hypotheses and
// contested claims (knowledge-gaps), how stale a belief's evidence is
// (trust freshness), how uncertain it is (trust score), and how well a
// source's stated confidence tracks reality (calibration). None of that,
// on its own, tells an agent WHERE to look next. This package ranks live
// beliefs by expected information gain — least confident, most stale, most
// contradicted first — and renders each as an actionable item: a re-verify
// task or an agent-facing question. It is deliberately read-only: it
// surfaces what to learn; acting on it is the agent's job.
//
// The ranking reuses the same primitives the rest of Mnemos computes from
// (salience via internal/trust, the gap definitions of Memory.KnowledgeGaps,
// the freshness decay of internal/trust) rather than inventing a new notion
// of uncertainty.
package curiosity

import (
	"math"
	"sort"
)

// LowTrustFloor is the trust score at or below which a belief is uncertain
// enough to warrant re-acquisition even when it is neither a gap nor stale.
// It mirrors the low-trust threshold the `metrics` surface reports on.
const LowTrustFloor = 0.5

// Reason codes explain why a belief entered the acquisition queue. A belief may
// satisfy several; [Item.Reason] carries the most urgent (see reasonRank).
const (
	// ReasonContested — the belief has enough live contradiction edges to count
	// as contested knowledge that needs reconciling.
	ReasonContested = "contested"
	// ReasonUnresolvedHypothesis — a hypothesis with no validates/refutes
	// verdict: predicted but never confirmed or ruled out.
	ReasonUnresolvedHypothesis = "unresolved_hypothesis"
	// ReasonStale — the belief's freshest signal has decayed past the trust
	// staleness floor; it may no longer hold.
	ReasonStale = "stale"
	// ReasonLowTrust — the belief's composite trust score is low; it is weakly
	// corroborated / under-confident.
	ReasonLowTrust = "low_trust"
)

// Action codes name the acquisition step an item calls for.
const (
	// ActionResolve — reconcile a contradiction (contested beliefs).
	ActionResolve = "resolve"
	// ActionVerify — re-confirm or refute the belief against fresh evidence.
	ActionVerify = "verify"
)

// Belief is the per-claim uncertainty evidence the queue ranks on, assembled by
// the caller from the store's existing signals. Every field is already computed
// elsewhere in Mnemos; this struct just carries them into the ranking.
type Belief struct {
	// ClaimID / Text identify the belief.
	ClaimID string
	// Text is the belief statement.
	Text string
	// Trust is the composite trust score in [0,1]; 1−Trust is the uncertainty
	// the queue prioritizes on.
	Trust float64
	// Salience is the intrinsic importance in [0,1] (trust.SalienceOf) — a
	// high-stakes uncertain belief outranks a trivial one.
	Salience float64
	// Staleness is 1−freshness in [0,1] (trust.Staleness); higher = older signal.
	Staleness float64
	// StaleDays is the age in days of the belief's freshest signal, for display.
	StaleDays int
	// Contradictions is the number of live contradicts edges touching the belief.
	Contradictions int
	// Contested reports whether Contradictions crossed the gap threshold.
	Contested bool
	// Unresolved reports a hypothesis with no validates/refutes verdict.
	Unresolved bool
	// Stale reports whether trust.IsStale flagged the belief.
	Stale bool
	// SourceOverconfidence is the calibration over-confidence gap of the belief's
	// author in [0,1] (stated confidence minus observed accuracy, clamped at 0):
	// a chronically over-confident source's claims deserve extra scrutiny. 0 when
	// unknown or well-calibrated.
	SourceOverconfidence float64
}

// Item is one entry in the acquisition queue: a belief worth learning about or
// verifying next, with the reason it surfaced and a priority for ranking.
type Item struct {
	// ClaimID / Text identify the belief to act on.
	ClaimID string `json:"belief_id"`
	Text    string `json:"text"`
	// Reason is the single most urgent reason code; Reasons lists every one that
	// applied.
	Reason  string   `json:"reason"`
	Reasons []string `json:"reasons"`
	// Action is the acquisition step: resolve (a contradiction) or verify.
	Action string `json:"action"`
	// Question is an agent-facing prompt — the thing to go find out.
	Question string `json:"question"`
	// Priority is the ranking score (expected information gain); higher = do first.
	Priority float64 `json:"priority"`
	// Trust / Staleness / StaleDays / Contradictions echo the driving signals so a
	// consumer can render the "why" without recomputing.
	Trust          float64 `json:"trust"`
	Staleness      float64 `json:"staleness"`
	StaleDays      int     `json:"stale_days"`
	Contradictions int     `json:"contradictions"`
}

// reasonRank orders reason codes by urgency (lower = more urgent).
func reasonRank(code string) int {
	switch code {
	case ReasonContested:
		return 0
	case ReasonUnresolvedHypothesis:
		return 1
	case ReasonStale:
		return 2
	case ReasonLowTrust:
		return 3
	default:
		return 4
	}
}

// Prioritize ranks the beliefs into an acquisition queue: least confident, most
// stale, most contradicted first. A belief enters the queue only when it is
// worth acquiring — contested, an unresolved hypothesis, stale, or low-trust; a
// well-corroborated, fresh, high-trust belief has nothing to learn and is
// omitted. Ranking is by expected information gain:
//
//	priority = salience × uncertainty × (0.3 + 0.7·staleness)
//	           × contradiction pressure × unresolved bonus × over-confidence bump
//
// The salience × uncertainty × staleness core mirrors Memory.KnowledgeGaps'
// scoring, so gap items rank consistently whether read from that surface or here.
// limit ≤ 0 returns the whole queue. It never writes and never mutates its input.
func Prioritize(beliefs []Belief, limit int) []Item {
	items := make([]Item, 0, len(beliefs))
	for _, b := range beliefs {
		lowTrust := b.Trust <= LowTrustFloor
		if !b.Contested && !b.Unresolved && !b.Stale && !lowTrust {
			continue // nothing to acquire: fresh, trusted, uncontested
		}

		reasons := collectReasons(b, lowTrust)
		primary := reasons[0]

		uncertainty := clamp01(1 - b.Trust)
		// Staleness modulates but never zeroes: a fresh unresolved hypothesis is
		// still worth chasing (mirrors KnowledgeGaps' 0.3 + 0.7·staleness).
		priority := b.Salience * uncertainty * (0.3 + 0.7*clamp01(b.Staleness))
		if b.Contradictions > 0 {
			// Contradiction pressure, saturating at 4 edges (+60% max).
			priority *= 1 + 0.15*math.Min(float64(b.Contradictions), 4)
		}
		if b.Unresolved {
			priority *= 1.25 // predicted but never confirmed — chase it
		}
		if b.SourceOverconfidence > 0 {
			// An over-confident author's stated confidence is discounted, so its
			// beliefs deserve more scrutiny (+50% at full over-confidence).
			priority *= 1 + 0.5*clamp01(b.SourceOverconfidence)
		}

		items = append(items, Item{
			ClaimID:        b.ClaimID,
			Text:           b.Text,
			Reason:         primary,
			Reasons:        reasons,
			Action:         actionFor(primary),
			Question:       questionFor(b, primary),
			Priority:       priority,
			Trust:          b.Trust,
			Staleness:      b.Staleness,
			StaleDays:      b.StaleDays,
			Contradictions: b.Contradictions,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority > items[j].Priority
		}
		// Deterministic tiebreak: id, so equal-priority queues are stable.
		return items[i].ClaimID < items[j].ClaimID
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

// collectReasons returns every applicable reason code, most urgent first. The
// input already guarantees at least one applies.
func collectReasons(b Belief, lowTrust bool) []string {
	var reasons []string
	if b.Contested {
		reasons = append(reasons, ReasonContested)
	}
	if b.Unresolved {
		reasons = append(reasons, ReasonUnresolvedHypothesis)
	}
	if b.Stale {
		reasons = append(reasons, ReasonStale)
	}
	if lowTrust {
		reasons = append(reasons, ReasonLowTrust)
	}
	sort.SliceStable(reasons, func(i, j int) bool {
		return reasonRank(reasons[i]) < reasonRank(reasons[j])
	})
	return reasons
}

func actionFor(reason string) string {
	if reason == ReasonContested {
		return ActionResolve
	}
	return ActionVerify
}

// questionFor renders the agent-facing prompt for the primary reason.
func questionFor(b Belief, reason string) string {
	switch reason {
	case ReasonContested:
		return "Which is true — resolve the contradiction around: " + b.Text
	case ReasonUnresolvedHypothesis:
		return "What evidence confirms or refutes: " + b.Text
	case ReasonStale:
		return "Is this still true — re-verify: " + b.Text
	default: // ReasonLowTrust
		return "What corroborates or refutes: " + b.Text
	}
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
