package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The shipped observability bundle (ADR 0022) lives at repo-root deploy/observability.
// Tests run with CWD = this package dir (cmd/mnemos), so reach it via ../../.
const observabilityDir = "../../deploy/observability"

// metricRef matches a mnemos metric name (must have the mnemos_ prefix + a name
// body). It deliberately requires an underscore after "mnemos" so it does not
// match the dashboard's "mnemos-brain" uid, the "mnemos" tag, or "mnemos serve".
var metricRef = regexp.MustCompile(`mnemos_[a-z0-9_]+`)

// emittedMetricNames returns the set of metric family names actually exported on
// mnemosRegistry. The two HTTP *Vec metrics only appear in Gather() once they
// have at least one child series, so materialize one of each first — otherwise a
// dashboard/alert referencing them would look like drift when it is correct.
func emittedMetricNames(t *testing.T) map[string]bool {
	t.Helper()
	httpRequestsTotal.WithLabelValues("GET", "/", "200").Add(0)
	httpRequestDuration.WithLabelValues("GET", "/", "200").Observe(0)

	families, err := mnemosRegistry.Gather()
	if err != nil {
		t.Fatalf("gather mnemosRegistry: %v", err)
	}
	names := make(map[string]bool, len(families))
	for _, f := range families {
		names[f.GetName()] = true
	}
	// Sanity: the registry must expose the product gauges and RED metrics we
	// expect, or the drift check below is vacuous.
	for _, must := range []string{
		"mnemos_brain_health_status",
		"mnemos_beliefs_total",
		"mnemos_http_requests_total",
		"mnemos_http_request_duration_seconds",
	} {
		if !names[must] {
			t.Fatalf("expected %s on mnemosRegistry but it was absent (gathered %d families)", must, len(names))
		}
	}
	return names
}

// normalizeMetric strips the histogram exposition suffixes so a PromQL
// reference to mnemos_http_request_duration_seconds_bucket maps back to its
// Gather() family name mnemos_http_request_duration_seconds.
func normalizeMetric(name string) string {
	for _, suf := range []string{"_bucket", "_sum", "_count"} {
		if base, ok := strings.CutSuffix(name, suf); ok {
			return base
		}
	}
	return name
}

// assertNoMetricDrift extracts every mnemos_* reference from the given bundle
// file and fails if any is not actually emitted. This is the guarantee that a
// renamed/dropped metric breaks the build instead of silently rotting the
// dashboard or alerts.
func assertNoMetricDrift(t *testing.T, file string, emitted map[string]bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(observabilityDir, file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	refs := metricRef.FindAllString(string(raw), -1)
	if len(refs) == 0 {
		t.Fatalf("%s references no mnemos_* metrics — the extractor or the file is wrong", file)
	}
	seen := map[string]bool{}
	for _, r := range refs {
		n := normalizeMetric(r)
		if seen[n] {
			continue
		}
		seen[n] = true
		if !emitted[n] {
			t.Errorf("%s references metric %q (from %q) which is not emitted on mnemosRegistry — drift", file, n, r)
		}
	}
}

func TestObservabilityBundle_AlertsNoMetricDrift(t *testing.T) {
	assertNoMetricDrift(t, "alerts.yml", emittedMetricNames(t))
}

func TestObservabilityBundle_DashboardNoMetricDrift(t *testing.T) {
	assertNoMetricDrift(t, "grafana-dashboard.json", emittedMetricNames(t))
}

func TestObservabilityBundle_AlertsParse(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(observabilityDir, "alerts.yml"))
	if err != nil {
		t.Fatalf("read alerts.yml: %v", err)
	}
	var doc struct {
		Groups []struct {
			Name  string `yaml:"name"`
			Rules []struct {
				Alert string `yaml:"alert"`
				Expr  string `yaml:"expr"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("alerts.yml is not valid YAML: %v", err)
	}
	if len(doc.Groups) == 0 {
		t.Fatal("alerts.yml has no rule groups")
	}
	for _, g := range doc.Groups {
		if g.Name == "" {
			t.Error("alert group missing a name")
		}
		if len(g.Rules) == 0 {
			t.Errorf("alert group %q has no rules", g.Name)
		}
		for _, r := range g.Rules {
			if r.Alert == "" || strings.TrimSpace(r.Expr) == "" {
				t.Errorf("group %q has a rule with empty alert/expr", g.Name)
			}
		}
	}
}

func TestObservabilityBundle_DashboardParse(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(observabilityDir, "grafana-dashboard.json"))
	if err != nil {
		t.Fatalf("read grafana-dashboard.json: %v", err)
	}
	var doc struct {
		Title  string           `json:"title"`
		Panels []map[string]any `json:"panels"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("grafana-dashboard.json is not valid JSON: %v", err)
	}
	if doc.Title == "" {
		t.Error("dashboard has no title")
	}
	if len(doc.Panels) == 0 {
		t.Error("dashboard has no panels")
	}
}
