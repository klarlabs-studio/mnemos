package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Belief writes, governance, feedback, purge, Markdown export and entity
// associations over the REST routes that serve them. Go names use machine
// terms (Claim, Contradiction, EntityRelationship); the JSON keys are the
// server's brain-native wire names. Every call needs a token whose scope the
// route requires; an unknown claim is an *[APIError] with Status 404 and a
// malformed request one with Status 400.

// rawBody makes [Client.do] hand back the response bytes undecoded.
type rawBody []byte

// RememberClaimWithEvidence stores one fact as a claim, recording each
// non-blank evidence string as an observation event the claim links to, and
// returns the id of the claim holding it: a new claim, or the one the brain
// already held when the fact restated it. A zero validFrom means now.
// POST /v1/facts; needs claims:write and a token that is not run-scoped.
func (c *Client) RememberClaimWithEvidence(ctx context.Context, text string, evidence []string, validFrom time.Time) (string, error) {
	got, err := c.RememberClaimWithEvidenceDetailed(ctx, text, evidence, validFrom)
	return got.ID, err
}

// RememberedClaim reports where a remembered claim landed.
type RememberedClaim struct {
	// ID is the claim holding the belief.
	ID string `json:"belief_id"`
	// Known is true when the brain already held the belief, so the write
	// linked its evidence to the existing claim instead of adding one.
	Known bool `json:"known"`
}

// RememberClaimWithEvidenceDetailed is [Client.RememberClaimWithEvidence]
// reporting whether the brain already held the belief.
func (c *Client) RememberClaimWithEvidenceDetailed(ctx context.Context, text string, evidence []string, validFrom time.Time) (RememberedClaim, error) {
	body := map[string]any{"text": text, "evidence": evidence}
	if !validFrom.IsZero() {
		body["valid_from"] = validFrom.UTC().Format(time.RFC3339Nano)
	}
	var out RememberedClaim
	if err := c.do(ctx, http.MethodPost, "/v1/facts", body, &out); err != nil {
		return RememberedClaim{}, err
	}
	return out, nil
}

// ScanRequest is a valid-time window for [Client.Scan]. Zero bounds are open;
// a zero Limit is the server cap (1000).
type ScanRequest struct {
	ValidFrom  time.Time
	ValidUntil time.Time
	Limit      int
}

// Scan returns claims whose valid-time interval overlaps the window, ordered
// by valid_from. GET /v1/scan.
func (c *Client) Scan(ctx context.Context, req ScanRequest) ([]ClaimDetail, error) {
	q := url.Values{}
	if !req.ValidFrom.IsZero() {
		q.Set("valid_from", req.ValidFrom.UTC().Format(time.RFC3339Nano))
	}
	if !req.ValidUntil.IsZero() {
		q.Set("valid_until", req.ValidUntil.UTC().Format(time.RFC3339Nano))
	}
	if req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	var out struct {
		Beliefs []ClaimDetail `json:"beliefs"`
	}
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/scan", q), nil, &out); err != nil {
		return nil, err
	}
	return out.Beliefs, nil
}

// ClaimStatusChange is the receipt of [Client.DeprecateClaim].
type ClaimStatusChange struct {
	ClaimID   string `json:"belief_id"`
	OldStatus string `json:"old_status"`
	NewStatus string `json:"new_status"`
}

// ClaimRevision is the receipt of [Client.UpdateClaim].
type ClaimRevision struct {
	ClaimID    string  `json:"belief_id"`
	OldText    string  `json:"old_text"`
	NewText    string  `json:"new_text"`
	Confidence float64 `json:"confidence"`
}

// ClaimEscalation is the receipt of [Client.EscalateClaim].
type ClaimEscalation struct {
	ClaimID          string `json:"belief_id"`
	Action           string `json:"action"`
	EscalationReason string `json:"escalation_reason"`
	Rationale        string `json:"rationale"`
}

func (c *Client) editClaim(ctx context.Context, claimID, op string, body, dst any) error {
	return c.do(ctx, http.MethodPost, "/v1/beliefs/"+url.PathEscape(claimID)+"/"+op, body, dst)
}

// DeprecateClaim marks a claim deprecated, recording reason on the status
// transition. POST /v1/beliefs/{id}/deprecate; needs claims:write.
func (c *Client) DeprecateClaim(ctx context.Context, claimID, reason string) (*ClaimStatusChange, error) {
	var out ClaimStatusChange
	if err := c.editClaim(ctx, claimID, "deprecate", map[string]string{"reason": reason}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateClaim rewrites a claim's text, and its confidence when confidence >
// 0. POST /v1/beliefs/{id}/update; needs claims:write.
func (c *Client) UpdateClaim(ctx context.Context, claimID, text string, confidence float64, reason string) (*ClaimRevision, error) {
	body := map[string]any{"text": text, "reason": reason}
	if confidence > 0 {
		body["confidence"] = confidence
	}
	var out ClaimRevision
	if err := c.editClaim(ctx, claimID, "update", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResolveContradiction settles two contradicting claims: the winner becomes
// resolved, the loser deprecated. POST /v1/beliefs/{winner}/resolve; needs
// claims:write.
func (c *Client) ResolveContradiction(ctx context.Context, winnerID, loserID, reason string) error {
	return c.editClaim(ctx, winnerID, "resolve", map[string]string{"loser_id": loserID, "reason": reason}, nil)
}

// EscalateClaim asks for a human decision on a claim and returns the
// escalation; nothing is written. POST /v1/beliefs/{id}/escalate; needs
// claims:write.
func (c *Client) EscalateClaim(ctx context.Context, claimID, reason string) (*ClaimEscalation, error) {
	var out ClaimEscalation
	if err := c.editClaim(ctx, claimID, "escalate", map[string]string{"reason": reason}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// VerifyClaim re-verifies a claim against fresh evidence and returns the
// verification instant. POST /v1/beliefs/{id}/verify; needs promote:global.
func (c *Client) VerifyClaim(ctx context.Context, claimID string) (time.Time, error) {
	var out struct {
		VerifiedAt time.Time `json:"verified_at"`
	}
	if err := c.editClaim(ctx, claimID, "verify", nil, &out); err != nil {
		return time.Time{}, err
	}
	return out.VerifiedAt, nil
}

// FeedbackResult is a claim's state after [Client.RecordFeedback].
type FeedbackResult struct {
	ClaimID                string  `json:"claim_id"`
	Helpful                bool    `json:"helpful"`
	NewStatus              string  `json:"new_status"`
	NewConfidence          float64 `json:"new_confidence"`
	NegativeFeedbackStreak int     `json:"negative_feedback_streak"`
	HelpfulCount           int     `json:"helpful_count"`
	AutoContested          bool    `json:"auto_contested"`
}

// RecordFeedback records whether a recalled claim was useful. An unhelpful
// vote decays confidence and, on the server's threshold, contests the claim.
// POST /v1/beliefs/{id}/feedback; needs claims:write.
func (c *Client) RecordFeedback(ctx context.Context, claimID string, helpful bool, note string) (*FeedbackResult, error) {
	var out FeedbackResult
	body := map[string]any{"helpful": helpful, "note": note}
	if err := c.do(ctx, http.MethodPost, "/v1/beliefs/"+url.PathEscape(claimID)+"/feedback", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PurgeResult counts what [Client.PurgeClaims] removed.
type PurgeResult struct {
	RequestID            string `json:"request_id"`
	RunID                string `json:"run_id"`
	ClaimsDeleted        int    `json:"beliefs_deleted"`
	EventsDeleted        int    `json:"episodes_deleted"`
	EmbeddingsDeleted    int    `json:"embeddings_deleted"`
	RelationshipsDeleted int    `json:"associations_deleted"`
}

// PurgeClaims HARD-deletes every claim tied to runID's events, with their
// evidence, embeddings and relationships, and the run's events (the GDPR
// erasure endpoint). Claims also evidenced by another run survive. This
// cannot be undone. DELETE /v1/beliefs?run_id=; needs claims:write and, for a
// run-scoped token, runID in its runs.
func (c *Client) PurgeClaims(ctx context.Context, runID string) (*PurgeResult, error) {
	q := url.Values{}
	q.Set("run_id", runID)
	var out PurgeResult
	if err := c.do(ctx, http.MethodDelete, withQuery("/v1/beliefs", q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ExportClaimMarkdown renders a claim as Git-friendly Markdown with YAML
// frontmatter; withProvenance adds the trust-provenance annotations.
// GET /v1/beliefs/{id}/export.md.
func (c *Client) ExportClaimMarkdown(ctx context.Context, claimID string, withProvenance bool) (string, error) {
	q := url.Values{}
	if !withProvenance {
		q.Set("provenance", "false")
	}
	var out rawBody
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/beliefs/"+url.PathEscape(claimID)+"/export.md", q), nil, &out); err != nil {
		return "", err
	}
	return string(out), nil
}

// EntityRelationship is an entity-to-entity edge (action_of, outcome_of,
// validates, ...). CreatedAt is RFC 3339; parse it with [ParseTime].
type EntityRelationship struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	FromID    string `json:"from_id"`
	FromType  string `json:"from_type"`
	ToID      string `json:"to_id"`
	ToType    string `json:"to_type"`
	CreatedAt string `json:"created_at,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
}

// EntityRelationshipFilter narrows [Client.ListEntityRelationships]. EntityID
// and EntityType together select one entity's edges; Kind one edge kind.
type EntityRelationshipFilter struct {
	EntityID   string
	EntityType string
	Kind       string
	Limit      int
	Offset     int
}

// EntityRelationshipPage is one page of entity relationships.
type EntityRelationshipPage struct {
	Edges  []EntityRelationship `json:"edges"`
	Total  int                  `json:"total"`
	Limit  int                  `json:"limit"`
	Offset int                  `json:"offset"`
}

// ListEntityRelationships lists entity-to-entity edges.
// GET /v1/entity-associations.
func (c *Client) ListEntityRelationships(ctx context.Context, f EntityRelationshipFilter) (*EntityRelationshipPage, error) {
	q := url.Values{}
	for k, v := range map[string]string{"entity_id": f.EntityID, "entity_type": f.EntityType, "kind": f.Kind} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	if f.Offset > 0 {
		q.Set("offset", strconv.Itoa(f.Offset))
	}
	var out EntityRelationshipPage
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/entity-associations", q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AppendEntityRelationships writes entity-to-entity edges, idempotently by
// id, and returns how many were accepted. An empty CreatedAt means now and an
// empty CreatedBy the token's subject. POST /v1/entity-associations; needs
// relationships:write.
func (c *Client) AppendEntityRelationships(ctx context.Context, edges []EntityRelationship) (int, error) {
	var out struct {
		Accepted int `json:"accepted"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/entity-associations", map[string]any{"edges": edges}, &out); err != nil {
		return 0, err
	}
	return out.Accepted, nil
}
