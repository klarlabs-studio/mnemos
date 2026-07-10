package relate

import "strings"

// aspect classifies a claim's lexical-aspect signal — the stance it
// takes about the underlying event being completed, ongoing, planned,
// or never having happened. Two claims about the same subject whose
// aspects are mutually exclusive are a contradiction.
type aspect int

const (
	aspectUnknown aspect = iota
	aspectCompleted
	aspectOngoing
	aspectPlanned
	aspectNever
)

// completedMarkers indicate the underlying event has happened and is
// no longer in flight. Each entry is a stem; matching is done against
// the post-stem token set so "completed", "completes", and "complete"
// all match the same way the overlap path treats them.
var completedMarkers = map[string]struct{}{
	"complet":    {},
	"finish":     {},
	"done":       {},
	"succeed":    {},
	"shipp":      {},
	"deliver":    {},
	"resolv":     {},
	"end":        {},
	"clos":       {},
	"sign-off":   {},
	"signoff":    {},
	"rolledback": {},
	"deployed":   {},
}

// ongoingMarkers indicate the event is still in flight as of the
// claim's effective time. "still" is the most reliable signal in
// English; the others are common run-state verbs.
var ongoingMarkers = map[string]struct{}{
	"still":     {},
	"ongoing":   {},
	"continu":   {},
	"underway":  {},
	"runn":      {},
	"running":   {},
	"pending":   {},
	"inflight":  {},
	"inprogres": {},
	"working":   {},
	"workin":    {},
	"active":    {},
}

// plannedMarkers indicate a future / scheduled event.
//
// "forecast" is deliberately absent: in operational text it's rare as a
// planning verb ("planned"/"scheduled" dominate), while in product/marketing
// copy it's an ordinary capability verb ("forecasts next month's revenue")
// that carries no aspect claim about a shared event — reading it as an aspect
// produced spurious contradictions (see the mnemos issue on passive-mode
// contradiction over-triggering).
var plannedMarkers = map[string]struct{}{
	"plan":    {},
	"schedul": {},
	"upcom":   {},
	"will":    {},
}

// neverMarkers indicate the event explicitly did not (and will not)
// happen. Distinct from polarity negation, which the existing path
// already covers — these are dedicated lexical signals like "never".
// "abandon" is deliberately absent: its dominant real-world sense in product
// text is the e-commerce noun phrase "cart abandonment" — a tracked feature,
// not a claim that an event was cancelled — so it mis-classified ordinary
// claims as aspectNever (see the mnemos issue on passive-mode contradiction
// over-triggering). Keep the unambiguous never-signals.
var neverMarkers = map[string]struct{}{
	"never":    {},
	"cancel":   {},
	"declined": {},
}

// classifyAspect returns the dominant aspect of a claim by inspecting
// its raw lowercase tokens. Crucially this does NOT use
// rawContentTokens because that helper filters negation words like
// "never" — which carry the strongest aspect signal we want to read.
func classifyAspect(text string) aspect {
	tokens := map[string]struct{}{}
	for _, w := range strings.Fields(strings.ToLower(text)) {
		w = strings.Trim(w, ",.;:!?()[]{}\"'")
		if w == "" {
			continue
		}
		tokens[w] = struct{}{}
	}
	// Prefer stronger markers: never > planned > ongoing > completed.
	// Real claims that mix markers ("we planned the migration but it
	// completed early") are ambiguous; classifyAspect returns one
	// label, deferring to the strongest signal so the divergence
	// path stays conservative.
	for tok := range tokens {
		stem := stemWord(tok)
		if _, ok := neverMarkers[stem]; ok {
			return aspectNever
		}
		if _, ok := neverMarkers[tok]; ok {
			return aspectNever
		}
	}
	for tok := range tokens {
		stem := stemWord(tok)
		if _, ok := plannedMarkers[stem]; ok {
			return aspectPlanned
		}
		if _, ok := plannedMarkers[tok]; ok {
			return aspectPlanned
		}
	}
	for tok := range tokens {
		stem := stemWord(tok)
		if _, ok := ongoingMarkers[stem]; ok {
			return aspectOngoing
		}
		if _, ok := ongoingMarkers[tok]; ok {
			return aspectOngoing
		}
	}
	for tok := range tokens {
		stem := stemWord(tok)
		if _, ok := completedMarkers[stem]; ok {
			return aspectCompleted
		}
		if _, ok := completedMarkers[tok]; ok {
			return aspectCompleted
		}
	}
	return aspectUnknown
}

// aspectsConflict reports whether two aspects describe mutually
// exclusive states for the same event. Completed vs ongoing is the
// canonical case; planned vs completed is also a conflict (the event
// can't both be planned and already done). aspectUnknown never
// conflicts — too lossy a signal.
func aspectsConflict(a, b aspect) bool {
	if a == aspectUnknown || b == aspectUnknown || a == b {
		return false
	}
	// Treat (completed, ongoing), (completed, planned),
	// (completed, never), (ongoing, never), (planned, never),
	// (planned, ongoing) all as conflicts. Unknown → no signal.
	return true
}

// detectTemporalDivergence flags two claims as contradicting when
// they share a subject token but their aspects are mutually
// exclusive — e.g. "The migration completed on Tuesday" vs "The
// migration is still running".
//
// Acceptance threshold for the benchmark is 0.70 (lower than entity
// or numeric) because aspect signals are easier to spoof in real
// production text — claims often mix tenses ("we planned to ship and
// shipped"). The lexicon-based heuristic targets the clearest cases.
func detectTemporalDivergence(aText, bText string, aTokens, bTokens map[string]struct{}) bool {
	aAspect := classifyAspect(aText)
	bAspect := classifyAspect(bText)
	if !aspectsConflict(aAspect, bAspect) {
		return false
	}

	// Require a shared subject anchor after stop-word removal. Without
	// this guard, "deploy completed" and "rollback still running" would
	// flag despite being about different operations.
	overlap := contentOverlap(aTokens, bTokens)
	if overlap < 1 {
		return false
	}
	// For long claims, a single shared token is usually coincidental —
	// most often a brand/entity name that appears in every claim of a
	// corpus (e.g. "CartLens forecasts revenue" vs "CartLens tracks cart
	// abandonment" share only "cartlens"). One token out of seven or
	// eight is not a shared subject, so require at least two before
	// trusting an aspect mismatch. Short claims (the canonical "migration
	// completed" vs "migration is still running") legitimately pivot on a
	// single subject token, so the stricter rule applies only when both
	// claims are long.
	shorter := min(len(aTokens), len(bTokens))
	if shorter >= longClaimTokenCount && overlap < 2 {
		return false
	}
	return true
}

// longClaimTokenCount is the content-token count at or above which a claim
// is "long" for the temporal-anchor guard: long claims must share more than
// one token to count as being about the same subject.
const longClaimTokenCount = 5
