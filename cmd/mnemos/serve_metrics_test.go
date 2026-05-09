package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServe_MetricsEndpointExposesPrometheusFormat(t *testing.T) {
	srv := httptest.NewServer(newServerMux(newServerTestStore_conn(t)))
	defer srv.Close()

	// Generate a request so the counter has a non-zero sample.
	if _, err := http.Get(srv.URL + "/health"); err != nil {
		t.Fatalf("seed request: %v", err)
	}

	resp, err := http.Get(srv.URL + "/internal/metrics")
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	required := []string{
		"mnemos_http_requests_total",
		"mnemos_http_request_duration_seconds",
		"mnemos_http_in_flight_requests",
	}
	for _, name := range required {
		if !strings.Contains(string(body), name) {
			t.Errorf("metrics body missing %q\n--- body ---\n%s", name, body)
		}
	}
}
