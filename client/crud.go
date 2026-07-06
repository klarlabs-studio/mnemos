package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// Claim CRUD parity (mnemos v0.67): single-claim Get, lifecycle transitions,
// novelty classification, and the decision browse surface over HTTP.

// GetClaim fetches a single claim's full detail, or nil on 404.
// GET /v1/claims/{id}.
func (c *Client) GetClaim(ctx context.Context, claimID string) (*ClaimDetail, error) {
	var out ClaimDetail
	if err := c.do(ctx, http.MethodGet, "/v1/claims/"+url.PathEscape(claimID), nil, &out); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// SetClaimLifecycle transitions a claim's lifecycle (candidate | promoted |
// superseded). POST /v1/claims/{id}/lifecycle.
func (c *Client) SetClaimLifecycle(ctx context.Context, claimID, lifecycle string) error {
	body := map[string]string{"lifecycle": lifecycle}
	return c.do(ctx, http.MethodPost, "/v1/claims/"+url.PathEscape(claimID)+"/lifecycle", body, nil)
}

// Classify reports whether a candidate statement fits established knowledge or
// is novel (deduplication / assimilation). GET /v1/classify?text=.
func (c *Client) Classify(ctx context.Context, text string) (*Assimilation, error) {
	q := url.Values{}
	q.Set("text", text)
	var out Assimilation
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/classify", q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListDecisions returns recorded decisions (browse). GET /v1/decisions.
func (c *Client) ListDecisions(ctx context.Context, limit int) ([]Decision, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Decisions []Decision `json:"decisions"`
	}
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/decisions", q), nil, &out); err != nil {
		return nil, err
	}
	return out.Decisions, nil
}

// GetDecision fetches a single decision, or nil on 404. GET /v1/decisions/{id}.
func (c *Client) GetDecision(ctx context.Context, id string) (*Decision, error) {
	var out Decision
	if err := c.do(ctx, http.MethodGet, "/v1/decisions/"+url.PathEscape(id), nil, &out); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}
