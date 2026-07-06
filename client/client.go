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
	"os"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/bolt"
	"go.klarlabs.de/fortify/retry"
)

// EnvBaseURL is the environment variable NewFromEnv reads for the registry
// base URL.
const EnvBaseURL = "MNEMOS_URL"

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
//
// An empty baseURL yields a DISABLED client: every call is a graceful
// no-op (reads return zero values, writes are accepted, neither errors),
// and IsEnabled reports false. This lets a consumer treat Mnemos as
// optional without guarding each call — see NewFromEnv.
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

// NewFromEnv constructs a Client from the MNEMOS_URL environment variable,
// with a sensible default retry policy (3 attempts, exponential backoff with
// jitter) already applied — caller options are appended, so a WithRetry of
// your own still wins.
//
// When MNEMOS_URL is unset the returned client is DISABLED: a safe no-op
// whose IsEnabled reports false. A consumer for whom Mnemos is optional can
// therefore wire the client unconditionally and never nil-check or
// feature-flag at the call site.
func NewFromEnv(opts ...Option) *Client {
	defaults := []Option{WithRetry(retry.Config{
		MaxAttempts:   3,
		InitialDelay:  200 * time.Millisecond,
		MaxDelay:      time.Second,
		BackoffPolicy: retry.BackoffExponential,
		Jitter:        true,
	})}
	return New(os.Getenv(EnvBaseURL), append(defaults, opts...)...)
}

// IsEnabled reports whether the client points at a registry. A nil client
// or one built with an empty base URL (e.g. NewFromEnv with MNEMOS_URL
// unset) is disabled — every call is a no-op.
func (c *Client) IsEnabled() bool { return c != nil && c.baseURL != "" }

// ForRun returns a view of the client pre-scoped to a single run_id — the
// tenant/session/subject boundary. Its Events and Claims builders come with
// the run_id filter already applied, so a caller partitioning memory by run
// doesn't repeat .RunID(...) on every read. The view shares the parent's
// transport, auth, retry, and disabled state.
func (c *Client) ForRun(runID string) *ScopedClient {
	return &ScopedClient{c: c, runID: runID}
}

// ScopedClient is a Client view bound to one run_id (see Client.ForRun).
type ScopedClient struct {
	c     *Client
	runID string
}

// IsEnabled reports whether the underlying client is enabled.
func (s *ScopedClient) IsEnabled() bool { return s.c.IsEnabled() }

// Events returns an events builder pre-filtered to this view's run_id.
func (s *ScopedClient) Events() *EventsBuilder { return s.c.Events().RunID(s.runID) }

// Claims returns a claims builder pre-filtered to this view's run_id.
func (s *ScopedClient) Claims() *ClaimsBuilder { return s.c.Claims().RunID(s.runID) }

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

// SetExpectation attaches (or replaces) a structured forward expectation on a
// claim: its value should land within tolerance of predicted by horizon. Pass a
// zero horizon to leave it open-ended. POST /v1/claims/{id}/expectation.
func (c *Client) SetExpectation(ctx context.Context, claimID string, predicted, tolerance float64, horizon time.Time) (*Expectation, error) {
	body := map[string]any{"predicted": predicted, "tolerance": tolerance}
	if !horizon.IsZero() {
		body["horizon"] = horizon.UTC().Format(time.RFC3339)
	}
	var out Expectation
	if err := c.do(ctx, http.MethodPost, "/v1/claims/"+url.PathEscape(claimID)+"/expectation", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RecordObservation records the observed value against a claim's expectation, so
// the nightly consolidate can reconcile it into a validates/refutes verdict.
// POST /v1/claims/{id}/observation.
func (c *Client) RecordObservation(ctx context.Context, claimID string, observed float64) (*Expectation, error) {
	var out Expectation
	if err := c.do(ctx, http.MethodPost, "/v1/claims/"+url.PathEscape(claimID)+"/observation", map[string]any{"observed": observed}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Expectation returns a claim's forward expectation (predicted vs observed), or
// nil when none is set. GET /v1/claims/{id}/expectation.
func (c *Client) Expectation(ctx context.Context, claimID string) (*Expectation, error) {
	var out Expectation
	if err := c.do(ctx, http.MethodGet, "/v1/claims/"+url.PathEscape(claimID)+"/expectation", nil, &out); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// do is the single point that sends a request, decodes a response, and
// applies cross-cutting concerns (logging + retry). All builder
// terminal verbs funnel through here.
func (c *Client) do(ctx context.Context, method, path string, body, dst any) error {
	// Disabled (no base URL) or nil client: no-op. Every terminal verb
	// funnels through here, so this is the single point that makes the whole
	// client safe to call when Mnemos is optional/unconfigured — reads leave
	// dst at its zero value, writes are accepted as no-ops, neither errors.
	if c == nil || c.baseURL == "" {
		return nil
	}

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
