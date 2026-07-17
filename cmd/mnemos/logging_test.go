package main

import (
	"bytes"
	"strings"
	"testing"

	"go.klarlabs.de/bolt"
	mnemos "go.klarlabs.de/mnemos"
)

func TestMnemosLogLevel(t *testing.T) {
	cases := map[string]bolt.Level{
		"":      bolt.INFO,
		"info":  bolt.INFO,
		"DEBUG": bolt.DEBUG,
		"warn":  bolt.WARN,
		"error": bolt.ERROR,
		"trace": bolt.TRACE,
		"bogus": bolt.INFO,
	}
	for in, want := range cases {
		t.Setenv("MNEMOS_LOG_LEVEL", in)
		if got := mnemosLogLevel(); got != want {
			t.Errorf("MNEMOS_LOG_LEVEL=%q → %v, want %v", in, got, want)
		}
	}
}

// TestLogHealthDegradation covers the ADR-0021 alerting log: healthy is silent; a
// degraded/unhealthy verdict emits a structured line carrying only the failing signals.
func TestLogHealthDegradation(t *testing.T) {
	// Healthy → nothing.
	var buf bytes.Buffer
	logger := bolt.New(bolt.NewJSONHandler(&buf))
	logHealthDegradation(logger, mnemos.BrainHealth{Status: mnemos.HealthOK})
	if buf.Len() != 0 {
		t.Errorf("healthy brain should log nothing, got %q", buf.String())
	}

	// Unhealthy with a dangling-edge pathology → an error line naming it.
	buf.Reset()
	logHealthDegradation(logger, mnemos.BrainHealth{
		Status: mnemos.HealthUnhealthy,
		Vitals: []mnemos.Vital{
			{Name: "free_energy", Value: 0.1, Status: mnemos.HealthOK},
			{Name: "low_trust", Value: 0.8, Status: mnemos.HealthUnhealthy},
		},
		Pathologies: []mnemos.Pathology{
			{Kind: "dangling_edges", Count: 3, Status: mnemos.HealthUnhealthy},
			{Kind: "orphan_claims", Count: 0, Status: mnemos.HealthOK},
		},
	})
	out := buf.String()
	if !strings.Contains(out, "brain health degraded") || !strings.Contains(out, `"level":"error"`) {
		t.Errorf("expected an error-level degradation line, got %q", out)
	}
	if !strings.Contains(out, "dangling_edges") || !strings.Contains(out, "low_trust") {
		t.Errorf("degradation line should name the failing signals, got %q", out)
	}
	if strings.Contains(out, "orphan_claims") || strings.Contains(out, "free_energy") {
		t.Errorf("degradation line should omit healthy signals, got %q", out)
	}
}
