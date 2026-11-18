package domain

import (
	"fmt"
	"strings"
	"time"
)

// Closed links and edges (ADR 0036 §8).
//
// Retraction (ADR 0037) and semantic-dedupe merges (ADR 0033) end evidence
// links and associations the brain held. Deleting them made the past
// unreconstructable: a recorded-as-of read before the change could not see
// what was removed. They are closed instead. The live row leaves the live
// table (claim_evidence, relationships), so no live read can see it, and is
// kept with when, by whom and why it was closed in a closed table
// (closed_claim_evidence, closed_relationships) that only recorded-as-of
// reads consult. A closed row was in force over [CreatedAt, ClosedAt).
//
// History is append-only: restoring a closed link (un-retraction, ADR 0037
// §8) writes a new live link with its own CreatedAt and leaves the closed
// interval where it is.

// ClosedEvidence is an evidence link that was in force until ClosedAt.
type ClosedEvidence struct {
	BeliefEvidence
	ClosedAt time.Time
	ClosedBy string
	Reason   string
}

// InForceAt reports whether the link was in force at t: recorded at or before
// t (a link with no recorded time is left to recorded.LinkRecordedAt) and not
// yet closed.
func (e ClosedEvidence) InForceAt(t time.Time) bool {
	return e.ClosedAt.After(t) && (e.CreatedAt.IsZero() || !e.CreatedAt.After(t))
}

// ClosedAssociation is an association that was in force until ClosedAt, with
// the endpoints it had when it was closed.
type ClosedAssociation struct {
	Association
	ClosedAt time.Time
	ClosedBy string
	Reason   string
}

// InForceAt reports whether the edge was in force at t.
func (a ClosedAssociation) InForceAt(t time.Time) bool {
	return a.ClosedAt.After(t) && !a.CreatedAt.After(t)
}

// ClosedRelationship is the machine-term alias of ClosedAssociation.
type ClosedRelationship = ClosedAssociation

// IsMergeClosureOf reports whether reason is the one the merge of claimID
// recorded when it closed claimID's own evidence links or edges.
func IsMergeClosureOf(claimID, reason string) bool {
	return strings.HasPrefix(reason, "merged "+claimID+" into ")
}

// MergeClosedReason is the reason recorded on what a merge of loserID into
// winnerID closed: each of the loser's evidence links (the winner gains the
// episode as a new link), and each association it dropped (a self-loop between
// loser and winner, or a duplicate of an edge the winner already had).
func MergeClosedReason(loserID, winnerID string) string {
	return fmt.Sprintf("merged %s into %s", loserID, winnerID)
}
