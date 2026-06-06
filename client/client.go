package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/bolt"
	"go.klarlabs.de/fortify/retry"
)

// Client is a typed Go client for the Mnemos registry HTTP API. It is
// safe for concurrent use by multiple goroutines.
//
// The fluent surface groups every endpoint under a resource accessor:
//
//	c.Events().List(ctx)                       // GET  /v1/events
//	c.Events().Append(ctx, events)             // POST /v1/events
//	c.Claims().Type("decision").Limit(25).List(ctx)
//	c.Claims().Append(ctx, claims, evidence)
//	c.Relationships().Type("contradicts").List(ctx)
//	c.Embeddings().Append(ctx, embs)
//
// Cross-cutting behavior — auth, structured logging via bolt, and
// retry-with-backoff via fortify — is configured once at construction
// and applied to every request.
type Client struct {
	baseURL string
	token   string
	httpc   *http.Client
	logger  *bolt.Logger
	retrier retry.Retry[*http.Response]
}

// Option configures a Client at construction time.
type Option func(*Client)

// WithToken sets the bearer token sent on write requests. Reads are open
// regardless. If the server has MNEMOS_REGISTRY_TOKEN set, this must
// match.
func WithToken(t string) Option {
	return func(c *Client) { c.token = t }
}

// WithHTTPClient swaps in a caller-provided *http.Client. Useful for
// tests, custom transports, or shared connection pools.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpc = h }
}

// WithTimeout sets the request timeout on the default HTTP client. Has
// no effect if WithHTTPClient was also passed — provide your own client
// with the timeout you want in that case.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if c.httpc == nil {
			c.httpc = &http.Client{Timeout: d}
			return
		}
		c.httpc.Timeout = d
	}
}

// WithLogger wires a bolt logger into the client. Every request emits
// an info-level event with method, path, status, and duration; failures
// emit at error level. Pass a nil-checked logger or use bolt.New for
// the default JSON handler.
func WithLogger(l *bolt.Logger) Option {
	return func(c *Client) { c.logger = l }
}

// WithRetry wraps every request in fortify's retry policy. By default
// the client makes a single attempt; pass a retry.Config to enable
// backoff. The client retries on network errors, 5xx, and 429; 2xx and
// other 4xx codes pass through immediately so client mistakes don't
// hammer the server.
func WithRetry(cfg retry.Config) Option {
	return func(c *Client) {
		c.retrier = retry.New[*http.Response](cfg)
	}
}

// New returns a Client pointing at the given Mnemos registry base URL
// (e.g. "http://localhost:7777"). Trailing slash is stripped. Default
// timeout is 30s; pass WithTimeout to change it.
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpc:   &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Health hits GET /health.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	var out HealthResponse
	if err := c.do(ctx, http.MethodGet, "/health", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Metrics hits GET /v1/metrics.
func (c *Client) Metrics(ctx context.Context) (*MetricsResponse, error) {
	var out MetricsResponse
	if err := c.do(ctx, http.MethodGet, "/v1/metrics", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Events returns a fluent builder for the /v1/events endpoint.
func (c *Client) Events() *EventsBuilder { return &EventsBuilder{c: c} }

// Claims returns a fluent builder for the /v1/claims endpoint.
func (c *Client) Claims() *ClaimsBuilder { return &ClaimsBuilder{c: c} }

// Relationships returns a fluent builder for the /v1/relationships endpoint.
func (c *Client) Relationships() *RelationshipsBuilder { return &RelationshipsBuilder{c: c} }

// Embeddings returns a fluent builder for the /v1/embeddings endpoint.
func (c *Client) Embeddings() *EmbeddingsBuilder { return &EmbeddingsBuilder{c: c} }

// SearchOptions tunes a Search call.
type SearchOptions struct {
	RunID        string
	TopK         int
	MinTrust     float64
	AsOf         string // RFC3339, validity time
	RecordedAsOf string // RFC3339, ingestion time
}

// SearchResponse is the shape of /v1/search.
type SearchResponse struct {
	Query          string                 `json:"query"`
	TopK           int                    `json:"top_k"`
	Total          int                    `json:"total"`
	Claims         []SearchClaim          `json:"claims"`
	Contradictions []SearchContradiction  `json:"contradictions"`
	AppliedFilters map[string]interface{} `json:"applied_filters,omitempty"`
}

// SearchClaim is a slim DTO returned by /v1/search.
type SearchClaim struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	Type       string  `json:"type"`
	Status     string  `json:"status"`
	Confidence float64 `json:"confidence"`
	TrustScore float64 `json:"trust_score"`
}

// SearchContradiction mirrors the relationships row format used by
// /v1/search responses.
type SearchContradiction struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	FromClaimID string `json:"from_claim_id"`
	ToClaimID   string `json:"to_claim_id"`
	CreatedAt   string `json:"created_at"`
}

// Search hits GET /v1/search. Hybrid retrieval over the claim store.
func (c *Client) Search(ctx context.Context, query string, opts SearchOptions) (*SearchResponse, error) {
	v := url.Values{}
	v.Set("query", query)
	if opts.TopK > 0 {
		v.Set("top_k", strconv.Itoa(opts.TopK))
	}
	if opts.RunID != "" {
		v.Set("run_id", opts.RunID)
	}
	if opts.MinTrust > 0 {
		v.Set("min_trust", strconv.FormatFloat(opts.MinTrust, 'f', -1, 64))
	}
	if opts.AsOf != "" {
		v.Set("as_of", opts.AsOf)
	}
	if opts.RecordedAsOf != "" {
		v.Set("recorded_as_of", opts.RecordedAsOf)
	}
	var out SearchResponse
	if err := c.do(ctx, http.MethodGet, "/v1/search?"+v.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ContextOptions tunes a Context call.
type ContextOptions struct {
	Query     string
	MaxTokens int
}

// contextResponse mirrors the /v1/context body. Returned as a string
// from Context() — callers wanting the run_id back can cast.
type contextResponse struct {
	RunID   string `json:"run_id"`
	Context string `json:"context"`
}

// Context hits GET /v1/context. Returns the rendered Context Block
// string for a run — drop directly into a system prompt.
func (c *Client) Context(ctx context.Context, runID string, opts ContextOptions) (string, error) {
	if runID == "" {
		return "", fmt.Errorf("Context: runID is required")
	}
	v := url.Values{}
	v.Set("run_id", runID)
	if opts.Query != "" {
		v.Set("query", opts.Query)
	}
	if opts.MaxTokens > 0 {
		v.Set("max_tokens", strconv.Itoa(opts.MaxTokens))
	}
	var out contextResponse
	if err := c.do(ctx, http.MethodGet, "/v1/context?"+v.Encode(), nil, &out); err != nil {
		return "", err
	}
	return out.Context, nil
}

// do is the single point that sends a request, decodes a response, and
// applies cross-cutting concerns (logging + retry). All builder
// terminal verbs funnel through here.
func (c *Client) do(ctx context.Context, method, path string, body, dst any) error {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
	}

	send := func(ctx context.Context) (*http.Response, error) {
		var reader io.Reader
		if bodyBytes != nil {
			reader = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		if reader != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, err := c.httpc.Do(req)
		if err != nil {
			return nil, err
		}
		// Surface retry-eligible HTTP statuses as errors so fortify
		// retries them; 2xx and non-retryable 4xx return cleanly.
		if shouldRetryStatus(resp.StatusCode) {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, &retryableHTTPError{status: resp.StatusCode, body: body}
		}
		return resp, nil
	}

	start := time.Now()
	var resp *http.Response
	var err error
	if c.retrier != nil {
		resp, err = c.retrier.Execute(ctx, send)
	} else {
		resp, err = send(ctx)
	}
	elapsed := time.Since(start)

	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	c.logRequest(method, path, status, elapsed, err)

	if err != nil {
		// retryableHTTPError carries the server status; surface as APIError.
		var rh *retryableHTTPError
		if errors.As(err, &rh) {
			return &APIError{Status: rh.status, Message: fmt.Sprintf("%s %s: %d %s", method, path, rh.status, parseErrorMessage(rh.body))}
		}
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return fmt.Errorf("read response body: %w", readErr)
	}

	if resp.StatusCode/100 != 2 {
		return &APIError{Status: resp.StatusCode, Message: fmt.Sprintf("%s %s: %d %s", method, path, resp.StatusCode, parseErrorMessage(respBody))}
	}

	if dst == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, dst); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

func (c *Client) logRequest(method, path string, status int, elapsed time.Duration, err error) {
	if c.logger == nil {
		return
	}
	event := c.logger.Info()
	if err != nil {
		event = c.logger.Error()
	}
	event.
		Str("method", method).
		Str("path", path).
		Int("status", status).
		Dur("duration", elapsed)
	if err != nil {
		event = event.Err(err)
	}
	event.Msg("mnemos client request")
}

// shouldRetryStatus returns true for HTTP statuses worth retrying:
// 5xx server errors and 429 rate-limit. Other 4xx codes indicate
// caller mistakes and should fail fast.
func shouldRetryStatus(code int) bool {
	if code >= 500 && code <= 599 {
		return true
	}
	return code == http.StatusTooManyRequests
}

// retryableHTTPError carries a status code through fortify's retry loop
// so the outer do() can convert it to an APIError after retries are
// exhausted.
type retryableHTTPError struct {
	status int
	body   []byte
}

func (e *retryableHTTPError) Error() string {
	return fmt.Sprintf("retryable HTTP %d", e.status)
}

func parseErrorMessage(body []byte) string {
	var errBody struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &errBody) == nil && errBody.Error != "" {
		return errBody.Error
	}
	return strings.TrimSpace(string(body))
}
