package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/narration"
	"go.klarlabs.de/mnemos/internal/pipeline"
)

// markerJudge drops every statement containing marker and records what it was
// asked, so a test can tell "the judge kept it" from "nobody asked the judge".
type markerJudge struct {
	marker string
	asked  []string
}

func (m *markerJudge) Classify(_ context.Context, texts []string) ([]narration.Verdict, error) {
	m.asked = append(m.asked, texts...)
	out := make([]narration.Verdict, len(texts))
	for i, t := range texts {
		if strings.Contains(t, m.marker) {
			out[i] = narration.Drop
		} else {
			out[i] = narration.Keep
		}
	}
	return out, nil
}

func (m *markerJudge) Identity() string { return "marker|judge" }

// End to end through process_text, the path the capture hook writes on: a
// session capture with an LLM in use must put its claims to the narration judge
// before storing them, and must not store what the judge drops. Unit tests of
// filterSessionNarration cannot catch the call being removed from
// mcpRunProcessText; this one does.
func TestE2E_ProcessTextSessionCaptureRunsTheNarrationJudge(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "capture.db"))
	t.Setenv("MNEMOS_NARRATION_FILTER", "") // the default: on

	// The LLM path of process_text, driven by the rule-based extractor so the
	// test needs no LLM.
	origExtractor, origJudge := newProcessTextExtractor, newCaptureNarrationJudge
	t.Cleanup(func() { newProcessTextExtractor, newCaptureNarrationJudge = origExtractor, origJudge })
	newProcessTextExtractor = func(bool) (*pipeline.Extractor, error) { return pipeline.NewExtractor(false) }
	judge := &markerJudge{marker: "assertion"}
	newCaptureNarrationJudge = func() (narrationClassifier, error) { return judge, nil }

	text := "We decided to run every release through kiln.\n" +
		"I am fixing the assertion in the outbox test now."
	in := mcpProcessTextInput{Text: text, SessionID: "session-narrated", UseLLM: true}
	if _, err := mcpRunProcessText(ctx, captureActor, in); err != nil {
		t.Fatal(err)
	}

	if len(judge.asked) == 0 {
		t.Fatal("process_text stored a session capture without asking the narration judge")
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
	var keptDurable bool
	for _, c := range claims {
		if strings.Contains(c.Text, "assertion") {
			t.Errorf("narration the judge dropped was stored: %q", c.Text)
		}
		if strings.Contains(c.Text, "kiln") {
			keptDurable = true
		}
	}
	if !keptDurable {
		t.Errorf("the durable claim the judge kept was not stored; claims = %v", claimTexts(claims))
	}
	// The judge must have seen the narration claim, or "not stored" above
	// would only mean the extractor never produced it.
	var askedNarration bool
	for _, a := range judge.asked {
		if strings.Contains(a, "assertion") {
			askedNarration = true
		}
	}
	if !askedNarration {
		t.Errorf("the judge was never asked about the narration claim; asked = %v", judge.asked)
	}
}

func claimTexts(cs []domain.Claim) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Text
	}
	return out
}
