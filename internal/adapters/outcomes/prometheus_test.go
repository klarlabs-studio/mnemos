package outcomes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestPrometheusAdapter_PullVector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"success",
			"data":{
				"resultType":"vector",
				"result":[{"metric":{},"value":[1714574400,"240"]}]
			}
		}`))
	}))
	defer srv.Close()
	a, err := NewPrometheus(PrometheusConfig{
		Endpoint: srv.URL,
		Queries:  map[string]string{"latency_p95_ms": `histogram_quantile(0.95, sum by (le) (rate(http_request_duration_seconds_bucket[5m]))) * 1000`},
		Now:      func() time.Time { return time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewPrometheus: %v", err)
	}
	out, err := a.Pull(context.Background(), "ac_xyz")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if out.ActionID != "ac_xyz" {
		t.Fatalf("ActionID: want ac_xyz, got %q", out.ActionID)
	}
	if out.Result != domain.OutcomeResultSuccess {
		t.Fatalf("Result: want success (no threshold), got %s", out.Result)
	}
	if got := out.Metrics["latency_p95_ms"]; got != 240 {
		t.Fatalf("latency_p95_ms: want 240, got %v", got)
	}
	if out.Source != "pull:prometheus" {
		t.Fatalf("Source: want pull:prometheus, got %q", out.Source)
	}
}

func TestPrometheusAdapter_FailureWhenAboveThreshold(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"status":"success",
			"data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1500"]}]}
		}`))
	}))
	defer srv.Close()
	a, err := NewPrometheus(PrometheusConfig{
		Endpoint:         srv.URL,
		Queries:          map[string]string{"latency_p95_ms": `q`},
		SuccessThreshold: map[string]float64{"latency_p95_ms": 1000},
		Now:              func() time.Time { return time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewPrometheus: %v", err)
	}
	out, err := a.Pull(context.Background(), "ac_xyz")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if out.Result != domain.OutcomeResultFailure {
		t.Fatalf("want failure (1500 > 1000), got %s", out.Result)
	}
}

func TestPrometheusAdapter_RejectsEmptyEndpoint(t *testing.T) {
	if _, err := NewPrometheus(PrometheusConfig{Queries: map[string]string{"x": "x"}}); err == nil {
		t.Fatal("expected error for empty endpoint")
	}
}

func TestPrometheusAdapter_RejectsNoQueries(t *testing.T) {
	if _, err := NewPrometheus(PrometheusConfig{Endpoint: "http://x"}); err == nil {
		t.Fatal("expected error for no queries")
	}
}
