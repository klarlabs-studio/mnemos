package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// PassagesRequest parameterizes a passage read.
type PassagesRequest struct {
	Query string
	Limit int    // 0 means the server default, 10
	RunID string // restricts to one run; required for a run-scoped token
}

// Passages returns the stored episode lines that best match a query, best
// first. GET /v1/passages.
func (c *Client) Passages(ctx context.Context, req PassagesRequest) ([]Passage, error) {
	q := url.Values{"query": {req.Query}}
	if req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	if req.RunID != "" {
		q.Set("run_id", req.RunID)
	}
	var out struct {
		Passages []Passage `json:"passages"`
	}
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/passages", q), nil, &out); err != nil {
		return nil, err
	}
	return out.Passages, nil
}
