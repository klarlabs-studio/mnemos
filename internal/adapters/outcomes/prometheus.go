// Package outcomes contains pull adapters that turn external metric
// or log sources into Mnemos Outcomes. Each adapter implements the
// Adapter interface so the runner can chain multiple sources behind
// a single CLI/MCP surface.
//
// The adapters are intentionally narrow: scrape, build Outcome,
// hand off. Persistence and ID generation are the caller's
// responsibility — the adapter knows nothing about the storage
// layer.
package outcomes

import (
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

	"go.klarlabs.de/mnemos/internal/domain"
)

// Adapter pulls metric observations from an external source and
// emits Outcomes. ActionID is the action being observed; the adapter
// names the metrics, the caller names the action.
type Adapter interface {
	Pull(ctx context.Context, actionID string) (domain.Outcome, error)
	Name() string
}

// PrometheusConfig configures the Prometheus instant-query adapter.
// Endpoint is the base URL of the Prometheus HTTP API (e.g.
// "http://prometheus.local:9090"); Queries maps a metric name (used
// as the Outcome.Metrics key) to a PromQL query string. Threshold
// classifies the outcome: any query returning a numeric value above
// SuccessThreshold[metric] (when set) flips the result to failure.
// Default verdict is success; explicit FailureMetrics takes
// precedence.
type PrometheusConfig struct {
	Endpoint string
	Queries  map[string]string
	// SuccessThreshold lets the caller declare "metric X must stay
	// below this value, otherwise mark the outcome as failure".
	// Empty map = success regardless of values (caller decides).
	SuccessThreshold map[string]float64
	// HTTPClient overrides the default *http.Client; tests inject
	// a transport that returns canned responses.
	HTTPClient *http.Client
	// Now overrides time.Now for deterministic tests.
	Now func() time.Time
}

// PrometheusAdapter implements Adapter against the Prometheus
// instant-query HTTP API.
type PrometheusAdapter struct {
	cfg PrometheusConfig
}

// NewPrometheus returns a configured adapter. Validates the endpoint
// URL and that at least one query is supplied.
func NewPrometheus(cfg PrometheusConfig) (*PrometheusAdapter, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("prometheus: endpoint is required")
	}
	if _, err := url.Parse(cfg.Endpoint); err != nil {
		return nil, fmt.Errorf("prometheus: invalid endpoint: %w", err)
	}
	if len(cfg.Queries) == 0 {
		return nil, errors.New("prometheus: at least one query is required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	return &PrometheusAdapter{cfg: cfg}, nil
}

// Name returns the source label written into Outcome.Source.
func (a *PrometheusAdapter) Name() string { return "pull:prometheus" }

// Pull executes every configured query in turn and bundles the
// results into a single Outcome. Each query failure surfaces a
// specific error so operators can correct PromQL without losing
// the partial metrics already collected.
func (a *PrometheusAdapter) Pull(ctx context.Context, actionID string) (domain.Outcome, error) {
	if strings.TrimSpace(actionID) == "" {
		return domain.Outcome{}, errors.New("prometheus: actionID is required")
	}
	metrics := make(map[string]float64, len(a.cfg.Queries))
	verdict := domain.OutcomeResultSuccess
	for name, query := range a.cfg.Queries {
		v, err := a.queryInstant(ctx, query)
		if err != nil {
			return domain.Outcome{}, fmt.Errorf("prometheus: query %q failed: %w", name, err)
		}
		metrics[name] = v
		if threshold, ok := a.cfg.SuccessThreshold[name]; ok && v > threshold {
			verdict = domain.OutcomeResultFailure
		}
	}
	now := a.cfg.Now().UTC()
	return domain.Outcome{
		ID:         "oc_pull_" + actionID + "_" + strconv.FormatInt(now.UnixNano(), 36),
		ActionID:   actionID,
		Result:     verdict,
		Metrics:    metrics,
		ObservedAt: now,
		Source:     a.Name(),
	}, nil
}

// queryInstant executes a Prometheus instant query and returns the
// scalar/vector result as a single float. For vector results, the
// first sample's value wins — the adapter is intentionally simple;
// callers wanting aggregates should use a PromQL aggregation in the
// query itself.
func (a *PrometheusAdapter) queryInstant(ctx context.Context, query string) (float64, error) {
	endpoint := strings.TrimRight(a.cfg.Endpoint, "/") + "/api/v1/query"
	q := url.Values{}
	q.Set("query", query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return 0, err
	}
	resp, err := a.cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("prometheus status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string            `json:"resultType"`
			Result     []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, fmt.Errorf("decode prometheus response: %w", err)
	}
	if payload.Status != "success" {
		return 0, fmt.Errorf("prometheus reported status %q", payload.Status)
	}
	if len(payload.Data.Result) == 0 {
		return 0, errors.New("prometheus returned an empty result set")
	}
	switch payload.Data.ResultType {
	case "scalar":
		// scalar: [<unix_time>, "<value>"]
		var pair [2]json.RawMessage
		if err := json.Unmarshal(payload.Data.Result[0], &pair); err != nil {
			return 0, fmt.Errorf("decode scalar: %w", err)
		}
		var s string
		if err := json.Unmarshal(pair[1], &s); err != nil {
			return 0, fmt.Errorf("decode scalar value: %w", err)
		}
		return strconv.ParseFloat(s, 64)
	case "vector":
		// vector: [{ "metric": {...}, "value": [<unix_time>, "<value>"] }, ...]
		var entry struct {
			Value [2]json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(payload.Data.Result[0], &entry); err != nil {
			return 0, fmt.Errorf("decode vector: %w", err)
		}
		var s string
		if err := json.Unmarshal(entry.Value[1], &s); err != nil {
			return 0, fmt.Errorf("decode vector value: %w", err)
		}
		return strconv.ParseFloat(s, 64)
	default:
		return 0, fmt.Errorf("unsupported prometheus resultType %q", payload.Data.ResultType)
	}
}
