package client

import (
	"context"
	"net/http"
	"net/url"
)

// EventUnretraction is the report POST /v1/episodes/{id}/unretract returns
// (ADR 0037 §8): the same document as `mnemos unretract-event --json` and the
// unretract_episode MCP tool. Go field names use machine terms; the JSON keys
// are the server's brain-native wire names.
type EventUnretraction struct {
	EventID string `json:"episode_id"`
	// UnretractedAt is RFC 3339; parse it with [ParseTime].
	UnretractedAt string `json:"unretracted_at"`
	UnretractedBy string `json:"unretracted_by"`
	Reason        string `json:"reason,omitempty"`
	// Generation is the generation of the cleared retraction (1 for the
	// event's first retraction).
	Generation int `json:"generation"`
	// RetractedAt (RFC 3339) and RetractedBy identify the cleared retraction.
	RetractedAt string `json:"retracted_at"`
	RetractedBy string `json:"retracted_by"`
	// AlreadyUnretracted is set when the retraction had been cleared before:
	// the stored record is returned and nothing was written.
	AlreadyUnretracted bool               `json:"already_unretracted"`
	Impact             UnretractionImpact `json:"impact"`
}

// UnretractionImpact is what an un-retraction changed, and what it left alone
// and why.
type UnretractionImpact struct {
	// Restored claims were deprecated by the retraction and are back at
	// their earlier status.
	Restored []RestoredClaim `json:"restored_beliefs"`
	// Skipped claims were changed after the retraction and left as they are.
	Skipped []SkippedClaim `json:"skipped_beliefs"`
	// Relinked claims have the event back as evidence and were rescored.
	Relinked    []RelinkedClaim `json:"relinked_beliefs"`
	Uncertainty []string        `json:"uncertainty"`
}

// RestoredClaim is a claim an un-retraction moved back to Status.
type RestoredClaim struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// SkippedClaim is a claim an un-retraction left alone, and why.
type SkippedClaim struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// RelinkedClaim is a claim that regained its evidence link to the event.
type RelinkedClaim struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	TrustBefore float64 `json:"trust_before"`
	TrustAfter  float64 `json:"trust_after"`
}

// UnretractEvent clears an event's retraction and returns the report.
// POST /v1/episodes/{id}/unretract; needs a token with claims:write. reason
// is optional. An unknown event — or one outside the token's runs — is an
// *[APIError] with Status 404; one that was never retracted is Status 409;
// repeating an un-retraction returns the stored record with
// AlreadyUnretracted set. A disabled client returns an empty report.
func (c *Client) UnretractEvent(ctx context.Context, eventID, reason string) (*EventUnretraction, error) {
	var body any
	if reason != "" {
		body = map[string]string{"reason": reason}
	}
	var out EventUnretraction
	if err := c.do(ctx, http.MethodPost, "/v1/episodes/"+url.PathEscape(eventID)+"/unretract", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
