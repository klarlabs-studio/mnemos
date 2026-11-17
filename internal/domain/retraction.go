package domain

import "time"

// JournalKindEventRetraction is one row per retracted event (ADR 0036): who
// retracted it, when, why, and the impact the retraction had. The subject is the
// event id and the row id is RetractionJournalID(eventID), so a retraction is
// recorded at most once.
const JournalKindEventRetraction = "event_retraction"

// RetractionJournalID is the journal row id of eventID's retraction. It is
// deterministic so the journal's idempotent-on-id append makes a second record
// of the same retraction impossible.
func RetractionJournalID(eventID string) string { return "retraction_" + eventID }

// RetractionReasonPrefix starts the status-history reason of every belief a
// retraction deprecates, so a retracted belief can be told apart from one an
// agent or a sweep deprecated.
const RetractionReasonPrefix = "retracted: "

// EventRetraction records that an event (an episode) was withdrawn as evidence
// (ADR 0036). The event row itself is immutable and stays; this record is the
// append-only fact that it no longer counts, plus what that changed.
type EventRetraction struct {
	EventID     string    `json:"episode_id"`
	RetractedAt time.Time `json:"retracted_at"`
	RetractedBy string    `json:"retracted_by"`
	Reason      string    `json:"reason"`
	// AlreadyRetracted is set on the report a repeated retraction returns: the
	// stored record is handed back and nothing was written. Never persisted.
	AlreadyRetracted bool             `json:"already_retracted,omitempty"`
	Impact           RetractionImpact `json:"impact"`
}

// RetractionImpact is the inspectable account of what a retraction touched. It
// lists what changed and what MAY be affected; it never asserts that a
// downstream decision or belief was wrong.
type RetractionImpact struct {
	// RetractedBeliefs had no live evidence left and moved to deprecated, with
	// a status-history row whose reason starts with RetractionReasonPrefix.
	RetractedBeliefs []string `json:"retracted_beliefs"`
	// AlreadyDeprecated were linked to the event but already deprecated; their
	// status was left alone.
	AlreadyDeprecated []string `json:"already_deprecated,omitempty"`
	// Rescored kept other live evidence: they lost the link to the event and
	// their trust was recomputed on what remains.
	Rescored []RescoredBelief `json:"rescored_beliefs"`
	// Associations are claim-to-claim edges touching a retracted belief. They
	// are kept for the audit trail; recall already excludes deprecated beliefs.
	Associations []string `json:"associations,omitempty"`
	// Decisions cited a retracted belief as a load-bearing input. Listed for
	// review, not judged: a decision may rest on other beliefs, and its own
	// outcome is what judges it.
	Decisions []DecisionExposure `json:"decisions,omitempty"`
	// OutcomeEdges are validates/refutes edges from outcomes onto a retracted
	// belief, kept as recorded.
	OutcomeEdges []string `json:"outcome_edges,omitempty"`
	// Uncertainty states what the report cannot see or does not claim.
	Uncertainty []string `json:"uncertainty"`
}

// RescoredBelief is a belief that survived a retraction on other evidence.
type RescoredBelief struct {
	ID                string  `json:"id"`
	TrustBefore       float64 `json:"trust_before"`
	TrustAfter        float64 `json:"trust_after"`
	RemainingEvidence int     `json:"remaining_evidence"`
}

// DecisionExposure names a decision and the retracted beliefs it cited.
type DecisionExposure struct {
	DecisionID string   `json:"decision_id"`
	Beliefs    []string `json:"beliefs"`
}
