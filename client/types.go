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

// Block is one working-memory block for an owner (GET/POST /v1/blocks).
type Block struct {
	Owner     string `json:"owner"`
	Label     string `json:"label"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// SynthesizeResult reports what a synthesize pass derived.
type SynthesizeResult struct {
	LessonsDerived   int `json:"lessons_derived"`
	PlaybooksDerived int `json:"playbooks_derived"`
}

// TimelineEvent is one event on the temporal timeline (GET /v1/timeline).
type TimelineEvent struct {
	ID       string            `json:"id"`
	At       string            `json:"at,omitempty"`
	Type     string            `json:"type"`
	Content  string            `json:"content"`
	RunID    string            `json:"run_id,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Signal is a detected temporal pattern (GET /v1/signals).
type Signal struct {
	Pattern     string  `json:"pattern"`
	RunID       string  `json:"run_id,omitempty"`
	Strength    float64 `json:"strength"`
	Confidence  float64 `json:"confidence"`
	Class       string  `json:"class,omitempty"`
	DetectedAt  string  `json:"detected_at,omitempty"`
	WindowStart string  `json:"window_start,omitempty"`
	WindowEnd   string  `json:"window_end,omitempty"`
}

// RecallResult is one retrieved claim from /v1/recall.
type RecallResult struct {
	ClaimID     string  `json:"claim_id"`
	Text        string  `json:"text"`
	Type        string  `json:"type"`
	Confidence  float64 `json:"confidence"`
	TrustScore  float64 `json:"trust_score"`
	HopDistance int     `json:"hop_distance"`
	Provenance  string  `json:"provenance,omitempty"`
}

// Sufficiency reports whether a recall's retrieved set is enough to answer
// (Sufficient) and how confident/how many claims back it.
type Sufficiency struct {
	Confidence float64 `json:"confidence"`
	ClaimCount int     `json:"claim_count"`
	Sufficient bool    `json:"sufficient"`
	Floor      float64 `json:"floor"`
}

// EffortReport is what a stakes-scaled recall actually spent.
type EffortReport struct {
	Budget  float64 `json:"budget"`
	Passes  int     `json:"passes"`
	Hops    int     `json:"hops"`
	Breadth int     `json:"breadth"`
}

// Conflict is a retrieved claim paired with a claim that contradicts it.
type Conflict struct {
	ClaimID            string  `json:"claim_id"`
	ClaimText          string  `json:"claim_text"`
	ContradictingID    string  `json:"contradicting_id"`
	ContradictingText  string  `json:"contradicting_text"`
	ContradictingTrust float64 `json:"contradicting_trust"`
}

// RecallResponse is the union result of /v1/recall — Results always, plus the
// mode-specific extras (Sufficiency/Effort/Conflicts/Rounds).
type RecallResponse struct {
	Mode        string         `json:"mode"`
	Results     []RecallResult `json:"results"`
	Sufficiency *Sufficiency   `json:"sufficiency,omitempty"`
	Effort      *EffortReport  `json:"effort,omitempty"`
	Conflicts   []Conflict     `json:"conflicts,omitempty"`
	Rounds      int            `json:"rounds,omitempty"`
}

// ClaimDetail is the full view of a single claim (GET /v1/claims/{id}) — the
// library's richer shape (statement, trust, lifecycle, validity window), distinct
// from the append/list Claim.
type ClaimDetail struct {
	ID         string  `json:"id"`
	Statement  string  `json:"statement"`
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence"`
	TrustScore float64 `json:"trust_score"`
	Lifecycle  string  `json:"lifecycle,omitempty"`
	ValidFrom  string  `json:"valid_from,omitempty"`
	ValidUntil string  `json:"valid_until,omitempty"`
	RecordedAt string  `json:"recorded_at,omitempty"`
}

// Assimilation is the novelty verdict for a candidate statement (POST /v1/classify):
// whether it Fits established knowledge (with the anchor it matches) or is Novel.
type Assimilation struct {
	Kind       string  `json:"kind"`
	AnchorID   string  `json:"anchor_id,omitempty"`
	AnchorText string  `json:"anchor_text,omitempty"`
	Confidence float64 `json:"confidence"`
}

// Decision is a recorded decision (GET /v1/decisions[/{id}]).
type Decision struct {
	ID           string   `json:"id"`
	Statement    string   `json:"statement"`
	Plan         string   `json:"plan,omitempty"`
	Reasoning    string   `json:"reasoning,omitempty"`
	RiskLevel    string   `json:"risk_level,omitempty"`
	Beliefs      []string `json:"beliefs,omitempty"`
	Alternatives []string `json:"alternatives,omitempty"`
	OutcomeID    string   `json:"outcome_id,omitempty"`
	ChosenAt     string   `json:"chosen_at,omitempty"`
}

// Expert is one worker's standing in the who-knows-what directory for a query:
// how strongly its memory matches (Affinity), how trustworthy those matching
// claims are (Reliability), and how many it authored (ClaimCount).
type Expert struct {
	Worker      string  `json:"worker"`
	Affinity    float64 `json:"affinity"`
	Reliability float64 `json:"reliability"`
	ClaimCount  int     `json:"claim_count"`
}

// Gap is a knowledge gap — an unresolved hypothesis or contested claim worth
// resolving, ranked by expected information gain (Score).
type Gap struct {
	ClaimID string  `json:"claim_id"`
	Text    string  `json:"text"`
	Kind    string  `json:"kind"`
	Score   float64 `json:"score"`
}

// CalibrationBucket partitions adjudicated claims by stated confidence and
// reports how often that confidence was borne out.
type CalibrationBucket struct {
	Lower          float64 `json:"lower"`
	Upper          float64 `json:"upper"`
	Count          int     `json:"count"`
	MeanConfidence float64 `json:"mean_confidence"`
	Accuracy       float64 `json:"accuracy"`
}

// CalibrationSource breaks calibration down by claim author — Gap > 0 means the
// source is over-confident.
type CalibrationSource struct {
	Source         string  `json:"source"`
	Samples        int     `json:"samples"`
	MeanConfidence float64 `json:"mean_confidence"`
	Accuracy       float64 `json:"accuracy"`
	Gap            float64 `json:"gap"`
	Brier          float64 `json:"brier"`
}

// Calibration reports how well stated confidence tracks reality across the
// store (ECE/Brier), bucketed and broken down by source.
type Calibration struct {
	Samples int                 `json:"samples"`
	ECE     float64             `json:"ece"`
	Brier   float64             `json:"brier"`
	Buckets []CalibrationBucket `json:"buckets"`
	Sources []CalibrationSource `json:"sources"`
}

// Hypercorrection flags an established (often promoted, high-trust) claim that a
// newer claim now contradicts — a belief worth re-examining.
type Hypercorrection struct {
	ContradictedClaimID  string  `json:"contradicted_claim_id"`
	ContradictedText     string  `json:"contradicted_text"`
	ContradictedTrust    float64 `json:"contradicted_trust"`
	ContradictedPromoted bool    `json:"contradicted_promoted"`
	ChallengingClaimID   string  `json:"challenging_claim_id"`
	ChallengingText      string  `json:"challenging_text"`
	DetectedAt           string  `json:"detected_at,omitempty"`
}

// Recombination is a topically-similar claim pair the store hasn't yet linked —
// a candidate novel connection.
type Recombination struct {
	ClaimA     string  `json:"claim_a"`
	TextA      string  `json:"text_a"`
	ClaimB     string  `json:"claim_b"`
	TextB      string  `json:"text_b"`
	Similarity float64 `json:"similarity"`
}

// Analogy is a claim structurally similar to a query claim (Weisfeiler-Lehman
// similarity over the evidence subgraph).
type Analogy struct {
	ClaimID    string  `json:"claim_id"`
	Text       string  `json:"text"`
	Similarity float64 `json:"similarity"`
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
