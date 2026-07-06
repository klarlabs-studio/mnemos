package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// RecallRequest parameterizes advanced recall (mnemos v0.68). Mode selects the
// epistemic-honesty variant; the mode-specific fields (Stakes / Context /
// MaxRounds) apply only to their mode.
type RecallRequest struct {
	Query string
	RunID string
	Limit int
	Hops  int
	// Mode: "sufficiency" (default) | "effort" | "context" | "conflicts" | "iterative".
	Mode string
	// Stakes scales the effort budget (mode="effort").
	Stakes float64
	// Context biases retrieval by a context string (mode="context").
	Context string
	// MaxRounds caps the retrieve<->reason iterations (mode="iterative").
	MaxRounds int
}

// Recall runs advanced retrieval and returns the union response: Results always,
// plus the mode-specific extras (Sufficiency for sufficiency/effort, Effort for
// effort, Conflicts for conflicts, Rounds for iterative). GET /v1/recall.
func (c *Client) Recall(ctx context.Context, req RecallRequest) (*RecallResponse, error) {
	q := url.Values{}
	q.Set("query", req.Query)
	if req.RunID != "" {
		q.Set("run_id", req.RunID)
	}
	if req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	if req.Hops > 0 {
		q.Set("hops", strconv.Itoa(req.Hops))
	}
	if req.Mode != "" {
		q.Set("mode", req.Mode)
	}
	switch req.Mode {
	case "effort":
		q.Set("stakes", strconv.FormatFloat(req.Stakes, 'f', -1, 64))
	case "context":
		q.Set("context", req.Context)
	case "iterative":
		if req.MaxRounds > 0 {
			q.Set("max_rounds", strconv.Itoa(req.MaxRounds))
		}
	}
	var out RecallResponse
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/recall", q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
