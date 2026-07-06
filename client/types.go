// Package client is a typed Go client for the Mnemos registry HTTP API
// (the surface served by `mnemos serve`). It mirrors the JSON shapes the
// server emits and accepts so callers don't have to construct or parse
// requests by hand.
//
// The types in this file are deliberately self-contained — they don't
// depend on Mnemos' internal domain types — so the client's stability is
// not coupled to internal refactors.
package client

import "time"

// Event is the wire shape for a single knowledge event.
type Event struct {
	ID            string            `json:"id"`
	RunID         string            `json:"run_id"`
	SchemaVersion string            `json:"schema_version"`
	Content       string            `json:"content"`
	SourceInputID string            `json:"source_input_id"`
	Timestamp     string            `json:"timestamp"`
	Metadata      map[string]string `json:"metadata"`
	IngestedAt    string            `json:"ingested_at"`
}

// Claim is the wire shape for a single extracted claim.
type Claim struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	Type       string  `json:"type"`       // fact | hypothesis | decision | test_result
	Confidence float64 `json:"confidence"` // 0.0 to 1.0
	Status     string  `json:"status"`     // active | contested | resolved | deprecated
	CreatedAt  string  `json:"created_at"`
	// CreatedBy attributes the claim to a specific author — a service's
	// sub-actor (a worker or, e.g., a coach). On Append it OVERRIDES the
	// client's token actor for this claim (the mnemos ClaimItem.CreatedBy
	// semantics), letting a per-org store shared across many authors attribute
	// each claim to the one that produced it — the substrate for who-knows-what,
	// per-source calibration, and independence grading. Empty → the token actor.
	// Echoed back on read.
	CreatedBy string `json:"created_by,omitempty"`
	// Visibility gates audience access: personal | team | org.
	// Omitted values are treated as "team" by the server.
	Visibility string `json:"visibility,omitempty"`
}

// Expectation is a structured forward expectation attached to a claim (T1.1):
// "this claim's value should land within Tolerance of Predicted by Horizon".
// Observed/HasObservation are set once the real value arrives; Resolved is true
// after the nightly consolidate reconciles it into a validates/refutes verdict.
type Expectation struct {
	ClaimID        string  `json:"claim_id"`
	Predicted      float64 `json:"predicted"`
	Tolerance      float64 `json:"tolerance"`
	Horizon        string  `json:"horizon,omitempty"` // RFC3339
	Observed       float64 `json:"observed,omitempty"`
	HasObservation bool    `json:"has_observation"`
	Resolved       bool    `json:"resolved"`
	CreatedAt      string  `json:"created_at,omitempty"`
}

// EvidenceLink ties a claim to one of the events it was extracted from.
type EvidenceLink struct {
	ClaimID string `json:"claim_id"`
	EventID string `json:"event_id"`
}

// Relationship represents a directed edge between two claims.
type Relationship struct {
	ID          string `json:"id"`
	Type        string `json:"type"` // supports | contradicts
	FromClaimID string `json:"from_claim_id"`
	ToClaimID   string `json:"to_claim_id"`
	CreatedAt   string `json:"created_at"`
}

// Embedding is a stored vector for an event or claim.
type Embedding struct {
	EntityID   string    `json:"entity_id"`
	EntityType string    `json:"entity_type"` // event | claim
	Vector     []float32 `json:"vector"`
	Model      string    `json:"model"`
	Dimensions int       `json:"dimensions"`
}

// HealthResponse is what GET /health returns.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// MetricsResponse is what GET /v1/metrics returns. Field names match the
// JSON exactly so callers reading individual fields don't need an extra
// mapping layer.
type MetricsResponse struct {
	Runs            int64 `json:"runs"`
	Events          int64 `json:"events"`
	Claims          int64 `json:"claims"`
	ContestedClaims int64 `json:"contested_claims"`
	Relationships   int64 `json:"relationships"`
	Contradictions  int64 `json:"contradictions"`
	Embeddings      int64 `json:"embeddings"`
}

// ListEventsResponse is what GET /v1/events returns.
type ListEventsResponse struct {
	Events []Event `json:"events"`
	Total  int     `json:"total"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
}

// ListClaimsResponse is what GET /v1/claims returns. Evidence links are
// included alongside the claims so a single round trip can recover the
// claim → event mapping that the local query engine depends on.
type ListClaimsResponse struct {
	Claims   []Claim        `json:"claims"`
	Evidence []EvidenceLink `json:"evidence,omitempty"`
	Total    int            `json:"total"`
	Limit    int            `json:"limit"`
	Offset   int            `json:"offset"`
}

// ListRelationshipsResponse is what GET /v1/relationships returns.
type ListRelationshipsResponse struct {
	Relationships []Relationship `json:"relationships"`
	Total         int            `json:"total"`
	Limit         int            `json:"limit"`
	Offset        int            `json:"offset"`
}

// ListEmbeddingsResponse is what GET /v1/embeddings returns.
type ListEmbeddingsResponse struct {
	Embeddings []Embedding `json:"embeddings"`
	Total      int         `json:"total"`
	Limit      int         `json:"limit"`
	Offset     int         `json:"offset"`
}

// AppendResponse is what POST endpoints return on 201.
type AppendResponse struct {
	Accepted int `json:"accepted"`
	Skipped  int `json:"skipped"`
}

// AppendClaimsBody is the request body for POST /v1/claims. Evidence is
// optional — pass nil if the claims don't have associated events yet.
type AppendClaimsBody struct {
	Claims   []Claim        `json:"claims"`
	Evidence []EvidenceLink `json:"evidence,omitempty"`
}

// APIError is returned from client methods when the server responds with
// a non-2xx status. Inspect Status for the HTTP code and Message for the
// server's error string.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return e.Message
}

// Helpers for parsing/formatting the timestamp strings the API uses.

// ParseTime parses an RFC 3339 timestamp from any of the time-shaped
// fields in this package. Use this when you need a time.Time rather than
// the raw string the API returns.
func ParseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

// FormatTime formats a time.Time the way the API expects on POST bodies.
func FormatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
