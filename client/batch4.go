package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Working memory + skill loop + temporal (mnemos v0.69): the last of the
// cognitive layer over HTTP.

// Blocks returns an owner's working-memory blocks. GET /v1/blocks?owner=.
func (c *Client) Blocks(ctx context.Context, owner string) ([]Block, error) {
	q := url.Values{}
	q.Set("owner", owner)
	var out struct {
		Blocks []Block `json:"blocks"`
	}
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/blocks", q), nil, &out); err != nil {
		return nil, err
	}
	return out.Blocks, nil
}

// SetBlock sets (replaces) a working-memory block. POST /v1/blocks.
func (c *Client) SetBlock(ctx context.Context, owner, label, value string) error {
	body := map[string]any{"owner": owner, "label": label, "value": value}
	return c.do(ctx, http.MethodPost, "/v1/blocks", body, nil)
}

// AppendBlock appends to a working-memory block. POST /v1/blocks (append=true).
func (c *Client) AppendBlock(ctx context.Context, owner, label, text string) error {
	body := map[string]any{"owner": owner, "label": label, "value": text, "append": true}
	return c.do(ctx, http.MethodPost, "/v1/blocks", body, nil)
}

// RecordAction records an operational action (skill-loop substrate) and returns
// its id. POST /v1/actions.
func (c *Client) RecordAction(ctx context.Context, kind, subject, actor string, metadata map[string]string) (string, error) {
	body := map[string]any{"kind": kind, "subject": subject, "actor": actor, "metadata": metadata}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/actions", body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// RecordActionOutcome records the observed result of an action.
// POST /v1/actions/{id}/outcome.
func (c *Client) RecordActionOutcome(ctx context.Context, actionID, result string, metrics map[string]float64, notes string) error {
	body := map[string]any{"result": result, "metrics": metrics, "notes": notes}
	return c.do(ctx, http.MethodPost, "/v1/actions/"+url.PathEscape(actionID)+"/outcome", body, nil)
}

// Synthesize runs a synthesis pass (actions → lessons + playbooks) and reports
// what it derived. POST /v1/synthesize.
func (c *Client) Synthesize(ctx context.Context) (*SynthesizeResult, error) {
	var out SynthesizeResult
	if err := c.do(ctx, http.MethodPost, "/v1/synthesize", struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TimelineRequest parameterizes a timeline read.
type TimelineRequest struct {
	RunID string
	From  string // RFC3339
	To    string // RFC3339
	Types []string
	Limit int
}

// Timeline returns events on the temporal timeline. GET /v1/timeline.
func (c *Client) Timeline(ctx context.Context, req TimelineRequest) ([]TimelineEvent, error) {
	q := url.Values{}
	if req.RunID != "" {
		q.Set("run_id", req.RunID)
	}
	if req.From != "" {
		q.Set("from", req.From)
	}
	if req.To != "" {
		q.Set("to", req.To)
	}
	if len(req.Types) > 0 {
		q.Set("types", strings.Join(req.Types, ","))
	}
	if req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	var out struct {
		Events []TimelineEvent `json:"events"`
	}
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/timeline", q), nil, &out); err != nil {
		return nil, err
	}
	return out.Events, nil
}

// Signals returns detected temporal patterns. GET /v1/signals.
func (c *Client) Signals(ctx context.Context, runID string, minConfidence float64, limit int) ([]Signal, error) {
	q := url.Values{}
	if runID != "" {
		q.Set("run_id", runID)
	}
	if minConfidence > 0 {
		q.Set("min_confidence", strconv.FormatFloat(minConfidence, 'f', -1, 64))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Signals []Signal `json:"signals"`
	}
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/signals", q), nil, &out); err != nil {
		return nil, err
	}
	return out.Signals, nil
}
