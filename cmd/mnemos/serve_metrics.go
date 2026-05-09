package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// RED metrics for the HTTP API. Naming follows the Prometheus Go
// instrumentation conventions: snake_case, units in suffix, type in
// suffix where ambiguous.
//
// Why a private registry? The default global prometheus registry is a
// shared process-wide singleton — bad for tests (re-registration
// panics), bad for libraries (anyone can scribble). The mnemos
// registry is owned by this package and exposed only at
// /internal/metrics.
var (
	mnemosRegistry = prometheus.NewRegistry()

	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mnemos_http_requests_total",
			Help: "Count of HTTP requests handled, partitioned by method, route, and status code.",
		},
		[]string{"method", "route", "status"},
	)

	httpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "mnemos_http_request_duration_seconds",
			Help: "Latency of HTTP requests in seconds. Buckets cover the SLO range (250ms p99 read, 500ms p99 write).",
			// Tight buckets at the SLO threshold; coarse beyond.
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0},
		},
		[]string{"method", "route", "status"},
	)

	httpInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "mnemos_http_in_flight_requests",
		Help: "Number of HTTP requests currently being handled.",
	})
)

func init() {
	mnemosRegistry.MustRegister(httpRequestsTotal, httpRequestDuration, httpInFlight)
}

// metricsMiddleware records RED telemetry (rate, errors, duration) for
// every request. Wraps the handler so each ServeHTTP invocation
// increments the in-flight gauge, records duration on exit, and bumps
// the request counter partitioned by status. Sits ahead of the access
// log so the duration matches what the log records.
//
// Route labels are extracted from the *registered* mux pattern via
// http.ServeMux.Handler — that's the canonical mnemos label and avoids
// the cardinality explosion of using the raw URL (which would create
// one series per /v1/claims/<id> call).
func metricsMiddleware(mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, route := mux.Handler(r)
		if route == "" {
			route = "unknown"
		}
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		httpInFlight.Inc()
		defer httpInFlight.Dec()
		next.ServeHTTP(rw, r)
		dur := time.Since(start).Seconds()
		status := strconv.Itoa(rw.status)
		httpRequestsTotal.WithLabelValues(r.Method, route, status).Inc()
		httpRequestDuration.WithLabelValues(r.Method, route, status).Observe(dur)
	})
}

// makeMnemosMetricsHandler returns the Prometheus exposition handler.
// Mounted at /internal/metrics — the leading "internal" segment
// signals this surface is for operators/scrapers, not end-users, and
// lets a fronting proxy easily allow-list or deny-list it separately
// from the public API.
func makeMnemosMetricsHandler() http.Handler {
	return promhttp.HandlerFor(mnemosRegistry, promhttp.HandlerOpts{
		// EnableOpenMetrics gives us exemplars and richer typing
		// without breaking text-format scrapers.
		EnableOpenMetrics: true,
	})
}
