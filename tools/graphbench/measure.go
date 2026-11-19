package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Stats is the latency summary of one (workload, variant) cell. Durations
// are milliseconds.
type Stats struct {
	Samples int     `json:"samples"`
	P50MS   float64 `json:"p50_ms"`
	P95MS   float64 `json:"p95_ms"`
	P99MS   float64 `json:"p99_ms"`
	MaxMS   float64 `json:"max_ms"`
	MeanMS  float64 `json:"mean_ms"`
}

// summarize computes nearest-rank percentiles. With fewer than 100 samples
// p99 is the maximum, so read it together with Samples.
func summarize(ds []time.Duration) Stats {
	if len(ds) == 0 {
		return Stats{}
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	var total time.Duration
	for _, d := range s {
		total += d
	}
	return Stats{
		Samples: len(s),
		P50MS:   msOf(percentile(s, 50)),
		P95MS:   msOf(percentile(s, 95)),
		P99MS:   msOf(percentile(s, 99)),
		MaxMS:   msOf(s[len(s)-1]),
		MeanMS:  msOf(total / time.Duration(len(s))),
	}
}

// percentile returns the nearest-rank p-th percentile of sorted.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	rank = min(max(rank, 1), len(sorted))
	return sorted[rank-1]
}

func msOf(d time.Duration) float64 {
	return math.Round(float64(d.Nanoseconds())/1e4) / 100
}

// canonical sorts a result's lines and hashes them, so two stores agree
// exactly when they returned the same set.
func canonical(lines []string) string {
	s := slices.Clone(lines)
	slices.Sort(s)
	h := sha256.New()
	for _, l := range s {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// loadAvg returns the 1-minute load average, or -1 when it cannot be read.
func loadAvg() float64 {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			return -1
		}
		return parseLoadAvg(string(b))
	}
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return -1
	}
	return parseLoadAvg(string(out))
}

// parseLoadAvg reads the first number from either "/proc/loadavg" ("0.52
// 0.40 0.31 1/123 4567") or darwin's "vm.loadavg" ("{ 0.52 0.40 0.31 }").
func parseLoadAvg(s string) float64 {
	for _, f := range strings.Fields(strings.Trim(strings.TrimSpace(s), "{}")) {
		if v, err := strconv.ParseFloat(f, 64); err == nil {
			return v
		}
	}
	return -1
}

// waitForQuietMachine blocks until the 1-minute load is at most maxLoad, so a
// concurrent workload on the machine does not corrupt the latency numbers. It
// gives up after limit and reports the load it gave up at.
func waitForQuietMachine(ctx context.Context, maxLoad float64, limit time.Duration, logf func(string, ...any)) (float64, error) {
	deadline := time.Now().Add(limit)
	for {
		l := loadAvg()
		if l < 0 || l <= maxLoad {
			return l, nil
		}
		if time.Now().After(deadline) {
			return l, fmt.Errorf("load %.1f still above %.1f after %s", l, maxLoad, limit)
		}
		logf("  load %.1f > %.1f, waiting %s", l, maxLoad, loadPoll)
		select {
		case <-ctx.Done():
			return l, ctx.Err()
		case <-time.After(loadPoll):
		}
	}
}

// loadPoll is how often waitForQuietMachine re-reads the load.
const loadPoll = 20 * time.Second
