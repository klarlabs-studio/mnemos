package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// JournalKindEventRetraction is one row per retracted event (ADR 0037): who
// retracted it, when, why, and the impact the retraction had. The subject is the
// event id and the row id is RetractionJournalID(eventID), so a retraction is
// recorded at most once.
const JournalKindEventRetraction = "event_retraction"

// RetractionJournalID is the journal row id of eventID's retraction. It is
// deterministic so the journal's idempotent-on-id append makes a second record
// of the same retraction impossible.
func RetractionJournalID(eventID string) string { return "retraction_" + eventID }

// RetractionJournalIDGen is the journal row id of eventID's gen-th retraction
// (ADR 0037 §8). The first keeps RetractionJournalID's id, so rows written
// before un-retraction existed need no migration.
func RetractionJournalIDGen(eventID string, gen int) string {
	if gen <= 1 {
		return RetractionJournalID(eventID)
	}
	return fmt.Sprintf("retraction_%s_%d", eventID, gen)
}

// JournalKindEventUnretraction is one row per cleared retraction (ADR 0037
// §8). The retraction row stays; this row records that it no longer holds.
const JournalKindEventUnretraction = "event_unretraction"

// UnretractionJournalID is the journal row id of the un-retraction that
// clears eventID's gen-th retraction.
func UnretractionJournalID(eventID string, gen int) string {
	if gen < 1 {
		gen = 1
	}
	return fmt.Sprintf("unretraction_%s_%d", eventID, gen)
}

// UnretractionReasonPrefix starts the status-history reason of every belief an
// un-retraction restores.
const UnretractionReasonPrefix = "unretracted: "

// RetractionReason is the exact status-history reason a retraction of eventID
// with the caller's reason writes on every belief it deprecates. Un-retraction
// matches it to recognise the retraction's own transition.
func RetractionReason(eventID, reason string) string {
	out := RetractionReasonPrefix + "evidence event " + eventID + " was retracted"
	if reason != "" {
		out += ": " + reason
	}
	return out
}

// UnretractionReason is the status-history reason of a belief restored by
// un-retracting eventID.
func UnretractionReason(eventID, reason string) string {
	out := UnretractionReasonPrefix + "evidence event " + eventID + " was restored"
	if reason != "" {
		out += ": " + reason
	}
	return out
}

// RetractionReasonPrefix starts the status-history reason of every belief a
// retraction deprecates, so a retracted belief can be told apart from one an
// agent or a sweep deprecated.
const RetractionReasonPrefix = "retracted: "

// EventRetraction records that an event (an episode) was withdrawn as evidence
// (ADR 0037). The event row itself is immutable and stays; this record is the
// append-only fact that it no longer counts, plus what that changed.
type EventRetraction struct {
	EventID     string    `json:"episode_id"`
	RetractedAt time.Time `json:"retracted_at"`
	RetractedBy string    `json:"retracted_by"`
	Reason      string    `json:"reason"`
	// Generation numbers the event's retractions from 1 (ADR 0037 §8). A row
	// written before un-retraction existed decodes as 0 and means 1; use Gen.
	Generation int `json:"generation,omitempty"`
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
	// DependentEpisodes are the episodes that depend on the retracted one,
	// directly or through other episodes (ADR 0038), each with the hop that
	// first reached it. They are not retracted themselves.
	DependentEpisodes []DependencyHop `json:"dependent_episodes,omitempty"`
	// BeliefPaths says, for every belief the retraction withdrew, rescored or
	// reached through a dependent episode and kept, the episode chain that
	// reached it (ADR 0038 §5).
	BeliefPaths []BeliefPath `json:"belief_paths,omitempty"`
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

// Gen is the retraction's generation, 1 for a row that predates generations.
func (r EventRetraction) Gen() int {
	if r.Generation < 1 {
		return 1
	}
	return r.Generation
}

// EventUnretraction records that an event's retraction was cleared (ADR 0037
// §8): the event counts as evidence again. Like the retraction it is an
// append-only fact; the retraction row it clears stays in the journal.
type EventUnretraction struct {
	EventID       string    `json:"episode_id"`
	UnretractedAt time.Time `json:"unretracted_at"`
	UnretractedBy string    `json:"unretracted_by"`
	Reason        string    `json:"reason"`
	// Generation is the generation of the retraction this clears.
	Generation int `json:"generation"`
	// RetractedAt and RetractedBy identify the cleared retraction.
	RetractedAt time.Time `json:"retracted_at"`
	RetractedBy string    `json:"retracted_by"`
	// AlreadyUnretracted is set on the report a repeated un-retraction
	// returns: the stored record is handed back and nothing was written.
	// Never persisted.
	AlreadyUnretracted bool               `json:"already_unretracted,omitempty"`
	Impact             UnretractionImpact `json:"impact"`
}

// UnretractionImpact is what an un-retraction changed, and what it left alone
// and why.
type UnretractionImpact struct {
	// Restored were deprecated by the cleared retraction and nothing else had
	// changed them since; each is back at its pre-retraction status.
	Restored []RestoredBelief `json:"restored_beliefs"`
	// Skipped were deprecated by the cleared retraction but are left as they
	// are, each with the reason.
	Skipped []SkippedBelief `json:"skipped_beliefs"`
	// Relinked were rescored by the retraction: they get the evidence link to
	// the event back and are rescored. Their status is not touched.
	Relinked []RelinkedBelief `json:"relinked_beliefs"`
	// Uncertainty states what the report cannot see or does not claim.
	Uncertainty []string `json:"uncertainty"`
}

// RestoredBelief is a belief an un-retraction moved back to Status.
type RestoredBelief struct {
	ID     string      `json:"id"`
	Status ClaimStatus `json:"status"`
}

// SkippedBelief is a belief an un-retraction left alone, and why.
type SkippedBelief struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// RelinkedBelief is a belief that regained its evidence link to the event.
type RelinkedBelief struct {
	ID          string      `json:"id"`
	Status      ClaimStatus `json:"status"`
	TrustBefore float64     `json:"trust_before"`
	TrustAfter  float64     `json:"trust_after"`
}

// RetractionHistory is one event's retractions and un-retractions, each sorted
// by generation (ADR 0037 §8).
type RetractionHistory struct {
	Retractions   []EventRetraction
	Unretractions []EventUnretraction
}

// FoldRetractionHistory groups retraction and un-retraction journal rows by
// event. Rows of any other kind are ignored.
func FoldRetractionHistory(entries []JournalEntry) (map[string]*RetractionHistory, error) {
	out := map[string]*RetractionHistory{}
	get := func(id string) *RetractionHistory {
		h := out[id]
		if h == nil {
			h = &RetractionHistory{}
			out[id] = h
		}
		return h
	}
	for _, e := range entries {
		switch e.Kind {
		case JournalKindEventRetraction:
			var rec EventRetraction
			if err := json.Unmarshal([]byte(e.Data), &rec); err != nil {
				return nil, fmt.Errorf("decode retraction %s: %w", e.ID, err)
			}
			h := get(rec.EventID)
			h.Retractions = append(h.Retractions, rec)
		case JournalKindEventUnretraction:
			var rec EventUnretraction
			if err := json.Unmarshal([]byte(e.Data), &rec); err != nil {
				return nil, fmt.Errorf("decode un-retraction %s: %w", e.ID, err)
			}
			h := get(rec.EventID)
			h.Unretractions = append(h.Unretractions, rec)
		}
	}
	for _, h := range out {
		sort.Slice(h.Retractions, func(i, j int) bool { return h.Retractions[i].Gen() < h.Retractions[j].Gen() })
		sort.Slice(h.Unretractions, func(i, j int) bool { return h.Unretractions[i].Generation < h.Unretractions[j].Generation })
	}
	return out, nil
}

// RetractedAt reports whether the event was retracted at instant at: the
// highest retraction generation recorded at or before at exceeds the highest
// un-retraction generation recorded at or before at. Generations alternate in
// time, so that is the state of the last transition at or before at. A zero at
// means now (every recorded row counts).
func (h RetractionHistory) RetractedAt(at time.Time) bool {
	maxR, maxU := 0, 0
	for _, r := range h.Retractions {
		if (at.IsZero() || !r.RetractedAt.After(at)) && r.Gen() > maxR {
			maxR = r.Gen()
		}
	}
	for _, u := range h.Unretractions {
		if (at.IsZero() || !u.UnretractedAt.After(at)) && u.Generation > maxU {
			maxU = u.Generation
		}
	}
	return maxR > maxU
}

// Active returns the retraction in force now, and false when the event is not
// retracted (never, or its latest retraction was cleared).
func (h RetractionHistory) Active() (EventRetraction, bool) {
	if len(h.Retractions) == 0 || !h.RetractedAt(time.Time{}) {
		return EventRetraction{}, false
	}
	return h.Retractions[len(h.Retractions)-1], true
}

// LastUnretraction returns the latest un-retraction, and false when there is
// none.
func (h RetractionHistory) LastUnretraction() (EventUnretraction, bool) {
	if len(h.Unretractions) == 0 {
		return EventUnretraction{}, false
	}
	return h.Unretractions[len(h.Unretractions)-1], true
}

// NextGeneration is the generation the event's next retraction takes.
func (h RetractionHistory) NextGeneration() int {
	if len(h.Retractions) == 0 {
		return 1
	}
	return h.Retractions[len(h.Retractions)-1].Gen() + 1
}
