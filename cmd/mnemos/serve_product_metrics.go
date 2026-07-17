package main

import (
	"context"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.klarlabs.de/bolt"
	mnemos "go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"
)

// Product + cognitive metrics (ADR 0020). Gauges, populated by a background sampler so a
// Prometheus scrape reads O(1) last-sampled values rather than triggering a full scan.
// Product vocabulary (beliefs/episodes/associations) to match the REST API.
const (
	metricsSampleDefaultInterval = 60 * time.Second
	// metricsLowTrustThreshold matches `mnemos metrics`: below this a belief is failing on
	// at least one of confidence, corroboration, or freshness.
	metricsLowTrustThreshold = 0.5
)

func newGauge(name, help string) prometheus.Gauge {
	return prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
}

var (
	gaugeBeliefs        = newGauge("mnemos_beliefs_total", "Current number of beliefs (claims) stored.")
	gaugeEpisodes       = newGauge("mnemos_episodes_total", "Current number of episodes (events) stored.")
	gaugeAssociations   = newGauge("mnemos_associations_total", "Current number of associations (relationships) stored.")
	gaugeContradictions = newGauge("mnemos_contradictions_total", "Current number of contradiction associations.")
	gaugeEmbeddings     = newGauge("mnemos_embeddings_total", "Current number of stored embeddings.")

	gaugeTrustAvg  = newGauge("mnemos_belief_trust_avg", "Mean trust score across beliefs, [0,1].")
	gaugeLowTrust  = newGauge("mnemos_low_trust_beliefs", "Beliefs below the low-trust threshold (0.5).")
	gaugeContested = newGauge("mnemos_contested_beliefs", "Beliefs currently in contested status.")

	gaugeFreeEnergy     = newGauge("mnemos_free_energy", "Hierarchical prediction-error aggregate, [0,1] (ADR 0017).")
	gaugeCalibrationECE = newGauge("mnemos_calibration_ece", "Expected calibration error, [0,1] (ADR 0013).")
	gaugeDissonance     = newGauge("mnemos_dissonance_rate", "Active hypercorrections per belief, [0,1].")
	gaugeStaleness      = newGauge("mnemos_staleness_rate", "Fraction of valid beliefs past the staleness horizon, [0,1].")
	gaugeHealthStatus   = newGauge("mnemos_brain_health_status", "Brain-health verdict: 0 healthy, 1 degraded, 2 unhealthy (ADR 0019).")

	gaugeOrphans           = newGauge("mnemos_orphan_beliefs", "Valid beliefs with zero evidence (integrity).")
	gaugeDangling          = newGauge("mnemos_dangling_associations", "Associations whose endpoint belief is missing (integrity).")
	gaugeStaleExpectations = newGauge("mnemos_stale_expectations", "Open predictions past their horizon (integrity).")

	gaugeSampleTimestamp = newGauge("mnemos_metrics_sample_timestamp_seconds", "Unix time of the last SUCCESSFUL product-metrics sample; alert on staleness.")
	gaugeSampleDuration  = newGauge("mnemos_metrics_sample_duration_seconds", "Wall-clock duration of the last product-metrics sample.")
	counterSampleErrors  = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "mnemos_metrics_sample_errors_total",
		Help: "Total product-metrics sampling errors (a sample never crashes serve).",
	})
)

func init() {
	mnemosRegistry.MustRegister(
		gaugeBeliefs, gaugeEpisodes, gaugeAssociations, gaugeContradictions, gaugeEmbeddings,
		gaugeTrustAvg, gaugeLowTrust, gaugeContested,
		gaugeFreeEnergy, gaugeCalibrationECE, gaugeDissonance, gaugeStaleness, gaugeHealthStatus,
		gaugeOrphans, gaugeDangling, gaugeStaleExpectations,
		gaugeSampleTimestamp, gaugeSampleDuration, counterSampleErrors,
	)
}

// metricsSampleInterval reads the sampler cadence (ADR 0020): MNEMOS_METRICS_SAMPLE_INTERVAL
// (Go duration; default 60s; 0 disables the sampler). Raise it for a large brain — each
// tick runs one full-scan BrainHealth.
func metricsSampleInterval() time.Duration {
	v := os.Getenv("MNEMOS_METRICS_SAMPLE_INTERVAL")
	if v == "" {
		return metricsSampleDefaultInterval
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return metricsSampleDefaultInterval
	}
	return d // 0 → disabled
}

// startProductMetricsSampler launches the background sampler (ADR 0020): it samples once
// immediately, then every interval, until ctx is cancelled. interval<=0 disables it. The
// logger carries the ADR-0021 health-degradation and sample-error logs.
func startProductMetricsSampler(ctx context.Context, conn *store.Conn, mem mnemos.Memory, interval time.Duration, logger *bolt.Logger) {
	if interval <= 0 || conn == nil {
		return
	}
	go func() {
		sampleProductMetrics(ctx, conn, mem, logger)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sampleProductMetrics(ctx, conn, mem, logger)
			}
		}
	}()
}

// sampleProductMetrics computes the product + cognitive metrics once and sets the gauges.
// Read-only and failure-isolated: a per-metric error increments the error counter and is
// skipped, leaving the last-good gauge value; a bad sample never crashes serve, and the
// success timestamp only advances when the whole sample was clean.
func sampleProductMetrics(ctx context.Context, conn *store.Conn, mem mnemos.Memory, logger *bolt.Logger) {
	start := time.Now()
	failed := false
	fail := func(err error) bool {
		if err != nil {
			failed = true
			counterSampleErrors.Inc()
			// Log the dropped error (ADR 0021) so a broken monitoring pipeline is visible.
			if logger != nil {
				logger.Warn().Err(err).Msg("mnemos: metrics sample error")
			}
			return true
		}
		return false
	}

	if n, err := conn.Events.CountAll(ctx); !fail(err) {
		gaugeEpisodes.Set(float64(n))
	}
	if n, err := conn.Claims.CountAll(ctx); !fail(err) {
		gaugeBeliefs.Set(float64(n))
	}
	if n, err := conn.Relationships.CountAll(ctx); !fail(err) {
		gaugeAssociations.Set(float64(n))
	}
	if n, err := conn.Relationships.CountByType(ctx, "contradicts"); !fail(err) {
		gaugeContradictions.Set(float64(n))
	}
	if conn.Embeddings != nil {
		if n, err := conn.Embeddings.CountAll(ctx); !fail(err) {
			gaugeEmbeddings.Set(float64(n))
		}
	}
	if scorer, ok := conn.Claims.(ports.TrustScorer); ok {
		if avg, err := scorer.AverageTrust(ctx); !fail(err) {
			gaugeTrustAvg.Set(avg)
		}
		if low, err := scorer.CountClaimsBelowTrust(ctx, metricsLowTrustThreshold); !fail(err) {
			gaugeLowTrust.Set(float64(low))
		}
	}
	if claims, err := conn.Claims.ListAll(ctx); !fail(err) {
		contested := 0.0
		for _, c := range claims {
			if string(c.Status) == "contested" {
				contested++
			}
		}
		gaugeContested.Set(contested)
	}

	// Cognitive + integrity, from the ADR-0019 BrainHealth (reused so gauges and the CLI
	// verdict agree). The one full-scan per tick; raise the interval for a large brain.
	if mem != nil {
		if h, err := mem.BrainHealth(ctx); !fail(err) {
			gaugeHealthStatus.Set(healthStatusCode(h.Status))
			for _, v := range h.Vitals {
				switch v.Name {
				case "free_energy":
					gaugeFreeEnergy.Set(v.Value)
				case "calibration":
					gaugeCalibrationECE.Set(v.Value)
				case "dissonance":
					gaugeDissonance.Set(v.Value)
				case "staleness":
					gaugeStaleness.Set(v.Value)
				}
			}
			for _, p := range h.Pathologies {
				switch p.Kind {
				case "orphan_claims":
					gaugeOrphans.Set(float64(p.Count))
				case "dangling_edges":
					gaugeDangling.Set(float64(p.Count))
				case "stale_expectations":
					gaugeStaleExpectations.Set(float64(p.Count))
				}
			}
			// Health-degradation log (ADR 0021): the primary alerting log, on the sampler
			// cadence.
			logHealthDegradation(logger, h)
		}
	}

	gaugeSampleDuration.Set(time.Since(start).Seconds())
	if !failed {
		gaugeSampleTimestamp.SetToCurrentTime()
	}
}

// logHealthDegradation emits the ADR-0021 alerting log when the brain is not healthy:
// warn when degraded, error when unhealthy, carrying only the failing vitals/pathologies.
// A no-op when healthy or when no logger is wired.
func logHealthDegradation(logger *bolt.Logger, h mnemos.BrainHealth) {
	if logger == nil || h.Status == mnemos.HealthOK {
		return
	}
	ev := logger.Warn()
	if h.Status == mnemos.HealthUnhealthy {
		ev = logger.Error()
	}
	ev = ev.Str("status", string(h.Status))
	for _, v := range h.Vitals {
		if v.Status != mnemos.HealthOK {
			ev = ev.Float64(v.Name, v.Value)
		}
	}
	for _, p := range h.Pathologies {
		if p.Status != mnemos.HealthOK {
			ev = ev.Int(p.Kind, p.Count)
		}
	}
	ev.Msg("mnemos: brain health degraded")
}

func healthStatusCode(s mnemos.HealthStatus) float64 {
	switch s {
	case mnemos.HealthUnhealthy:
		return 2
	case mnemos.HealthDegraded:
		return 1
	default:
		return 0
	}
}
