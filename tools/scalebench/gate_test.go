package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGate(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gate.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func gateReport(ops ...operation) Report {
	return Report{Params: reportParams{Beliefs: 1000, Shape: "uniform"}, Operations: ops}
}

// The gate passes within the ceilings and names every operation that is over
// one, failed, or was gated without being measured. It refuses a run at a size
// or shape the ceilings were not set for.
func TestCheckLimits(t *testing.T) {
	gate := writeGate(t, `{"beliefs":1000,"limits":{"uniform":{
		"ingest_remember":{"p50_ms":100,"p95_ms":200},
		"brain_health":{"p50_ms":500}}}}`)
	ok := gateReport(
		operation{Name: "ingest_remember", P50MS: 90, P95MS: 150, Outcome: "ok"},
		operation{Name: "brain_health", P50MS: 400, Outcome: "ok"},
		operation{Name: "recall_hops0", P50MS: 99999, Outcome: "ok"}, // not gated
	)
	if err := checkLimits(ok, gate); err != nil {
		t.Fatalf("within ceilings: %v", err)
	}
	cases := map[string]struct {
		rep  Report
		want string
	}{
		"p50 over":     {gateReport(operation{Name: "ingest_remember", P50MS: 101, P95MS: 150, Outcome: "ok"}, operation{Name: "brain_health", P50MS: 1, Outcome: "ok"}), "ingest_remember: p50"},
		"p95 over":     {gateReport(operation{Name: "ingest_remember", P50MS: 50, P95MS: 201, Outcome: "ok"}, operation{Name: "brain_health", P50MS: 1, Outcome: "ok"}), "ingest_remember: p95"},
		"timed out":    {gateReport(operation{Name: "ingest_remember", Outcome: "timeout"}, operation{Name: "brain_health", P50MS: 1, Outcome: "ok"}), "ingest_remember: timeout"},
		"not measured": {gateReport(operation{Name: "ingest_remember", P50MS: 1, P95MS: 1, Outcome: "ok"}), "brain_health: gated but not measured"},
	}
	for name, c := range cases {
		err := checkLimits(c.rep, gate)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, c.want)
		}
	}
	wrongSize := gateReport()
	wrongSize.Params.Beliefs = 2000
	if err := checkLimits(wrongSize, gate); err == nil {
		t.Error("a run at another size was compared")
	}
	wrongShape := gateReport()
	wrongShape.Params.Shape = "hub"
	if err := checkLimits(wrongShape, gate); err == nil {
		t.Error("a shape without ceilings was compared")
	}
}

// Repeated rounds fold to the median p50/p95, the worst max, and the first
// failure.
func TestMedianRounds(t *testing.T) {
	rounds := [][]operation{
		{{Name: "a", P50MS: 30, P95MS: 300, MaxMS: 310, Outcome: "ok"}},
		{{Name: "a", P50MS: 10, P95MS: 100, MaxMS: 900, Outcome: "ok"}},
		{{Name: "a", P50MS: 20, P95MS: 200, MaxMS: 210, Outcome: "timeout", Error: "slow"}},
	}
	got := medianRounds(rounds)
	if len(got) != 1 || got[0].P50MS != 20 || got[0].P95MS != 200 || got[0].MaxMS != 900 || got[0].Outcome != "timeout" {
		t.Fatalf("medianRounds = %+v", got)
	}
}
