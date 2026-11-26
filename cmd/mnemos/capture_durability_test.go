package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/pipeline"
)

// markerDurability labels every text containing marker session-local and the
// rest durable, recording what it was asked.
type markerDurability struct {
	marker string
	asked  []string
	err    error
}

func (m *markerDurability) classify(_ context.Context, texts []string) ([]extract.Durability, error) {
	m.asked = append(m.asked, texts...)
	if m.err != nil {
		return nil, m.err
	}
	out := make([]extract.Durability, len(texts))
	for i, t := range texts {
		if strings.Contains(t, m.marker) {
			out[i] = extract.DurabilitySessionLocal
		} else {
			out[i] = extract.DurabilityDurable
		}
	}
	return out, nil
}

// captureWithDurability runs process_text on the LLM path (driven by the
// rule-based extractor, so no LLM is needed) with the given classifier and
// returns the stored claims keyed by a word of their text.
func captureWithDurability(t *testing.T, cls *markerDurability, in mcpProcessTextInput) map[string]domain.Claim {
	t.Helper()
	ctx := context.Background()
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "capture.db"))
	t.Setenv("MNEMOS_NARRATION_FILTER", "off") // isolate the durability step

	origExtractor, origCls := newProcessTextExtractor, newCaptureDurabilityClassifier
	t.Cleanup(func() { newProcessTextExtractor, newCaptureDurabilityClassifier = origExtractor, origCls })
	newProcessTextExtractor = func(bool) (*pipeline.Extractor, error) { return pipeline.NewExtractor(false) }
	newCaptureDurabilityClassifier = func() (durabilityClassifier, error) { return cls.classify, nil }

	if _, err := mcpRunProcessText(ctx, captureActor, in); err != nil {
		t.Fatal(err)
	}
	conn, err := openConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	claims, err := conn.Claims.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]domain.Claim{}
	for _, c := range claims {
		for _, key := range []string{"kiln", "assertion"} {
			if strings.Contains(c.Text, key) {
				out[key] = c
			}
		}
	}
	return out
}

const durabilityCaptureText = "We decided to run every release through kiln.\n" +
	"I am fixing the assertion in the outbox test now."

// End to end through process_text, the path the capture hook writes on: a
// session capture stores each belief with the classifier's durability verdict,
// so recall's session-local demotion has something to act on from the moment a
// belief exists. Before this, only the bulk classify-durability pass set the
// field, and 88% of a real brain's live beliefs had no verdict.
func TestE2E_SessionCaptureStoresDurabilityVerdicts(t *testing.T) {
	t.Setenv("MNEMOS_CAPTURE_DURABILITY", "") // the default: on
	cls := &markerDurability{marker: "assertion"}
	got := captureWithDurability(t, cls, mcpProcessTextInput{
		Text: durabilityCaptureText, SessionID: "session-durability", UseLLM: true,
	})

	if len(cls.asked) == 0 {
		t.Fatal("a session capture was stored without asking the durability classifier")
	}
	if d := got["assertion"].Durability; d != domain.Durability(extract.DurabilitySessionLocal) {
		t.Errorf("progress report stored with durability %q, want session", d)
	}
	if d := got["kiln"].Durability; d != domain.Durability(extract.DurabilityDurable) {
		t.Errorf("decision stored with durability %q, want durable", d)
	}
}

// A classifier failure must never cost the capture: the beliefs are stored,
// unclassified, exactly as before this step existed.
func TestE2E_SessionCaptureKeepsBeliefsWhenDurabilityFails(t *testing.T) {
	t.Setenv("MNEMOS_CAPTURE_DURABILITY", "")
	cls := &markerDurability{marker: "assertion", err: errors.New("model unavailable")}
	got := captureWithDurability(t, cls, mcpProcessTextInput{
		Text: durabilityCaptureText, SessionID: "session-durability-down", UseLLM: true,
	})

	for _, key := range []string{"kiln", "assertion"} {
		c, ok := got[key]
		if !ok {
			t.Fatalf("belief about %s was lost when the classifier failed", key)
		}
		if c.Durability != "" {
			t.Errorf("belief about %s got durability %q from a failed classifier", key, c.Durability)
		}
	}
}

// Only session captures are classified: a document or a deliberate
// process_text call is not a transcript, and the prompt is written for one.
// MNEMOS_CAPTURE_DURABILITY=off turns the step off.
func TestE2E_DurabilityIsClassifiedOnlyForSessionCaptures(t *testing.T) {
	cases := []struct {
		name   string
		env    string
		input  mcpProcessTextInput
		wanted bool
	}{
		{"not a session", "", mcpProcessTextInput{Text: durabilityCaptureText, UseLLM: true}, false},
		{"no LLM", "", mcpProcessTextInput{Text: durabilityCaptureText, SessionID: "s-nollm"}, false},
		{"turned off", "off", mcpProcessTextInput{Text: durabilityCaptureText, SessionID: "s-off", UseLLM: true}, false},
		{"session capture", "", mcpProcessTextInput{Text: durabilityCaptureText, SessionID: "s-on", UseLLM: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MNEMOS_CAPTURE_DURABILITY", tc.env)
			cls := &markerDurability{marker: "assertion"}
			got := captureWithDurability(t, cls, tc.input)
			if asked := len(cls.asked) > 0; asked != tc.wanted {
				t.Fatalf("classifier asked = %v, want %v", asked, tc.wanted)
			}
			if !tc.wanted && got["assertion"].Durability != "" {
				t.Errorf("durability %q stored without classification", got["assertion"].Durability)
			}
		})
	}
}
