package client

import (
	"context"
	"net/http"
	"net/url"
)

// EventRetraction is the report POST /v1/episodes/{id}/retract returns
// (ADR 0037): the same document as `mnemos retract-event --json` and the
// retract_episode MCP tool. Go field names use machine terms; the JSON keys
// are the server's brain-native wire names.
type EventRetraction struct {
	EventID string `json:"episode_id"`
	// RetractedAt is RFC 3339; parse it with [ParseTime].
	RetractedAt string `json:"retracted_at"`
	RetractedBy string `json:"retracted_by"`
	Reason      string `json:"reason,omitempty"`
	// AlreadyRetracted is set when the event had been retracted before: the
	// stored record is returned and nothing was written.
	AlreadyRetracted bool             `json:"already_retracted"`
	Impact           RetractionImpact `json:"impact"`
}

// RetractionImpact lists what a retraction changed and what MAY be affected;
// it never asserts that a downstream decision or claim was wrong.
type RetractionImpact struct {
	// RetractedClaims had no live evidence left and were deprecated.
	RetractedClaims   []string        `json:"retracted_beliefs"`
	AlreadyDeprecated []string        `json:"already_deprecated,omitempty"`
	Rescored          []RescoredClaim `json:"rescored_beliefs"`
	// Relationships touching a retracted claim, kept for the audit trail.
	Relationships []string           `json:"associations,omitempty"`
	Decisions     []DecisionExposure `json:"decisions,omitempty"`
	OutcomeEdges  []string           `json:"outcome_edges,omitempty"`
	Uncertainty   []string           `json:"uncertainty"`
}

// RescoredClaim is a claim that survived a retraction on other evidence.
type RescoredClaim struct {
	ID                string  `json:"id"`
	TrustBefore       float64 `json:"trust_before"`
	TrustAfter        float64 `json:"trust_after"`
	RemainingEvidence int     `json:"remaining_evidence"`
}

// DecisionExposure names a decision and the retracted claims it cited.
type DecisionExposure struct {
	DecisionID string   `json:"decision_id"`
	Claims     []string `json:"beliefs"`
}

// RetractEvent withdraws an event (episode) as evidence and returns the impact
// report. POST /v1/episodes/{id}/retract; needs a token with claims:write.
// reason is optional. An unknown event — or one outside the token's runs — is
// an *[APIError] with Status 404; repeating a retraction returns the stored
// record with AlreadyRetracted set. A disabled client returns an empty report.
func (c *Client) RetractEvent(ctx context.Context, eventID, reason string) (*EventRetraction, error) {
	var body any
	if reason != "" {
		body = map[string]string{"reason": reason}
	}
	var out EventRetraction
	if err := c.do(ctx, http.MethodPost, "/v1/episodes/"+url.PathEscape(eventID)+"/retract", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
