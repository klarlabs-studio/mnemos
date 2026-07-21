package domain

import "strings"

// Durability classifies WHETHER a belief's value outlives the conversation it
// came from, so recall and contradiction detection can tell knowledge from the
// narration that surrounded it.
//
// It exists because capture admits far more than it should. Measured on a real
// brain the day incremental capture began working properly, intake went from
// roughly two claims a day to four thousand — 31% of the entire brain created
// in a single working day — and a hand-labelled sample put ~58% of it at
// session-local narration: progress reports, status snapshots, completion
// announcements. That material is not wrong, and deleting it would be an
// invisible loss of the real knowledge mixed in with it. But left
// indistinguishable from knowledge it competes at recall and, worse, argues
// with itself: two progress reports from different sessions look exactly like
// two beliefs in conflict.
//
// So this MARKS rather than drops (ADR 0023). Nothing is lost; session-local
// beliefs simply stop competing with knowledge and stop generating
// contradictions.
//
//   - DurabilityDurable — stays true and useful later: how a system behaves,
//     why something breaks, a decision and its reasoning, a constraint.
//   - DurabilitySessionLocal — tied to the moment it was written: "first test
//     fails", "CI passed in 1m59s", "merged as abc123".
//   - DurabilityUnknown — unclassified (the empty value). Treated exactly like
//     durable everywhere: an unclassified belief must never be quietly demoted,
//     because everything captured before this field existed is unclassified.
//
// The type is a string so it round-trips through JSON and DB columns and so an
// empty value naturally means unknown.
type Durability string

// Supported Durability values. The empty string is the canonical "unknown"
// value, so a zero Claim is unclassified and therefore treated as durable.
const (
	// DurabilityUnknown is the unclassified value (the empty string). It is
	// treated as durable: fail toward keeping a belief in play.
	DurabilityUnknown Durability = ""
	// DurabilityDurable marks knowledge whose value outlives its session.
	DurabilityDurable Durability = "durable"
	// DurabilitySessionLocal marks a statement tied to the moment it was
	// written. Still stored, still queryable, but excluded from contradiction
	// detection and demoted at recall.
	DurabilitySessionLocal Durability = "session"
)

// Normalized folds free-form input (case, surrounding whitespace) to a
// canonical Durability, mapping anything unrecognised to DurabilityUnknown.
// An unrecognised value must not silently demote a belief, so it degrades to
// unknown — which is treated as durable — rather than to session-local.
func (d Durability) Normalized() Durability {
	switch Durability(strings.ToLower(strings.TrimSpace(string(d)))) {
	case DurabilityDurable:
		return DurabilityDurable
	case DurabilitySessionLocal:
		return DurabilitySessionLocal
	default:
		return DurabilityUnknown
	}
}

// IsSessionLocal reports whether this belief is known to be tied to its
// session. Unknown is NOT session-local: the whole back catalogue is
// unclassified, and demoting it on absence of evidence would silently hide a
// brain's worth of knowledge.
func (d Durability) IsSessionLocal() bool {
	return d.Normalized() == DurabilitySessionLocal
}
