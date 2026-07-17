package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	mnemos "go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/domain"
)

// TestProductMetricsSampler_SetsGaugesAndExposes verifies ADR-0020: a sample sets the
// product gauges from the store and the cognitive gauges from BrainHealth, and the
// /internal/metrics endpoint exposes them for Prometheus/Grafana.
func TestProductMetricsSampler_SetsGaugesAndExposes(t *testing.T) {
	conn := newServerTestStore_conn(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedEventConn(t, conn, "ev1", "r", "alpha", "src1", "", now)
	seedEventConn(t, conn, "ev2", "r", "beta", "src2", "", now)
	seedClaimConn(t, conn, "c1", "belief one", string(domain.ClaimTypeFact), string(domain.ClaimStatusActive), 0.8, now)
	seedClaimConn(t, conn, "c2", "belief two", string(domain.ClaimTypeFact), string(domain.ClaimStatusContested), 0.4, now)

	// A separate empty memory exercises the cognitive branch (empty brain → healthy).
	mem, err := mnemos.New(mnemos.WithStorage("memory://?namespace=pmtest"), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatalf("New mem: %v", err)
	}
	defer func() { _ = mem.Close() }()

	sampleProductMetrics(ctx, conn, mem)

	if got := testutil.ToFloat64(gaugeBeliefs); got != 2 {
		t.Errorf("mnemos_beliefs_total = %v, want 2", got)
	}
	if got := testutil.ToFloat64(gaugeEpisodes); got != 2 {
		t.Errorf("mnemos_episodes_total = %v, want 2", got)
	}
	if got := testutil.ToFloat64(gaugeContested); got != 1 {
		t.Errorf("mnemos_contested_beliefs = %v, want 1", got)
	}
	if got := testutil.ToFloat64(gaugeHealthStatus); got != 0 {
		t.Errorf("mnemos_brain_health_status = %v, want 0 (empty brain healthy)", got)
	}
	if testutil.ToFloat64(gaugeSampleTimestamp) == 0 {
		t.Error("sample timestamp not advanced — the sample reported a failure")
	}

	// The endpoint exposes the new series for a Prometheus scrape.
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/internal/metrics")
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	for _, name := range []string{
		"mnemos_beliefs_total", "mnemos_belief_trust_avg", "mnemos_free_energy",
		"mnemos_brain_health_status", "mnemos_dangling_associations", "mnemos_metrics_sample_timestamp_seconds",
	} {
		if !strings.Contains(string(body), name) {
			t.Errorf("metrics body missing %q", name)
		}
	}
}

// TestMetricsSampleInterval covers the ADR-0020 cadence knob.
func TestMetricsSampleInterval(t *testing.T) {
	t.Setenv("MNEMOS_METRICS_SAMPLE_INTERVAL", "")
	if got := metricsSampleInterval(); got != metricsSampleDefaultInterval {
		t.Errorf("default = %v, want %v", got, metricsSampleDefaultInterval)
	}
	t.Setenv("MNEMOS_METRICS_SAMPLE_INTERVAL", "5m")
	if got := metricsSampleInterval(); got != 5*time.Minute {
		t.Errorf("5m = %v, want 5m", got)
	}
	t.Setenv("MNEMOS_METRICS_SAMPLE_INTERVAL", "0")
	if got := metricsSampleInterval(); got != 0 {
		t.Errorf("0 (disabled) = %v, want 0", got)
	}
	t.Setenv("MNEMOS_METRICS_SAMPLE_INTERVAL", "garbage")
	if got := metricsSampleInterval(); got != metricsSampleDefaultInterval {
		t.Errorf("garbage should fall back to default, got %v", got)
	}
}
