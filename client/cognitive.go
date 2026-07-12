package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// The connected-brain reads (mnemos v0.66): the library's cognitive layer over
// HTTP, so an out-of-process consumer gets who-knows-what, knowledge gaps,
// calibration, hypercorrections, recombinations, and analogical retrieval — not
// just storage. Each returns zero values on a disabled client.

// WhoKnows ranks the workers whose memory best matches a query — the
// who-knows-what directory. GET /v1/who-knows.
func (c *Client) WhoKnows(ctx context.Context, query string, limit int) ([]Expert, error) {
	q := url.Values{}
	q.Set("query", query)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Experts []Expert `json:"experts"`
	}
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/who-knows", q), nil, &out); err != nil {
		return nil, err
	}
	return out.Experts, nil
}

// KnowledgeGaps lists the store's highest-value open questions (unresolved
// hypotheses / contested claims). GET /v1/knowledge-gaps.
func (c *Client) KnowledgeGaps(ctx context.Context, limit int) ([]Gap, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Gaps []Gap `json:"gaps"`
	}
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/knowledge-gaps", q), nil, &out); err != nil {
		return nil, err
	}
	return out.Gaps, nil
}

// Calibration reports how well stated confidence tracks reality across the
// store, bucketed and broken down by source. GET /v1/calibration.
func (c *Client) Calibration(ctx context.Context) (*Calibration, error) {
	var out Calibration
	if err := c.do(ctx, http.MethodGet, "/v1/calibration", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Hypercorrections lists established beliefs a newer claim now contradicts —
// the things worth re-examining. GET /v1/hypercorrections.
func (c *Client) Hypercorrections(ctx context.Context) ([]Hypercorrection, error) {
	var out struct {
		Hypercorrections []Hypercorrection `json:"hypercorrections"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/hypercorrections", nil, &out); err != nil {
		return nil, err
	}
	return out.Hypercorrections, nil
}

// Recombinations lists topically-similar-but-unlinked claim pairs — candidate
// novel connections. GET /v1/recombinations.
func (c *Client) Recombinations(ctx context.Context, limit int) ([]Recombination, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Recombinations []Recombination `json:"recombinations"`
	}
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/recombinations", q), nil, &out); err != nil {
		return nil, err
	}
	return out.Recombinations, nil
}

// AnalogousClaims returns the claims most structurally analogous to a given one.
// GET /v1/claims/{id}/analogous.
func (c *Client) AnalogousClaims(ctx context.Context, claimID string, limit int) ([]Analogy, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Analogous []Analogy `json:"analogous"`
	}
	path := "/v1/beliefs/" + url.PathEscape(claimID) + "/analogous"
	if err := c.do(ctx, http.MethodGet, withQuery(path, q), nil, &out); err != nil {
		return nil, err
	}
	return out.Analogous, nil
}
