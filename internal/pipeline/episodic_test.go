package pipeline

import (
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
)

func TestTagEventEpisodes(t *testing.T) {
	events := []domain.Event{
		{ID: "e1", Content: "chunk with a release"},
		{ID: "e2", Content: "chunk with knowledge"},
		{ID: "e3", Content: "already typed", Metadata: map[string]string{"event_type": "manual"}},
	}
	claims := []domain.Claim{
		{ID: "c1", Type: domain.ClaimTypeFact, Text: "released v0.104.0 with 15 signed assets"},
		{ID: "c2", Type: domain.ClaimTypeFact, Text: "we chose Postgres because writes outgrew SQLite"},
		{ID: "c3", Type: domain.ClaimTypeFact, Text: "deployed v1.2.3 to production"},
	}
	links := []domain.ClaimEvidence{
		{ClaimID: "c1", EventID: "e1"},
		{ClaimID: "c2", EventID: "e2"},
		{ClaimID: "c3", EventID: "e3"},
	}
	tagEventEpisodes(events, claims, links)

	if got := events[0].Metadata["event_type"]; got != string(extract.EventRelease) {
		t.Errorf("e1 event_type = %q, want release", got)
	}
	if got := events[1].Metadata["event_type"]; got != "" {
		t.Errorf("e2 (knowledge) got tagged %q, want none", got)
	}
	// Must not overwrite an ingester-set type.
	if got := events[2].Metadata["event_type"]; got != "manual" {
		t.Errorf("e3 pre-set type overwritten: %q", got)
	}
}

// A batch with no events must be a clean no-op.
func TestTagEventEpisodes_NoEvents(t *testing.T) {
	events := []domain.Event{{ID: "e1", Content: "x"}}
	claims := []domain.Claim{{ID: "c1", Type: domain.ClaimTypeFact, Text: "the retry budget is 3 attempts"}}
	links := []domain.ClaimEvidence{{ClaimID: "c1", EventID: "e1"}}
	tagEventEpisodes(events, claims, links)
	if events[0].Metadata["event_type"] != "" {
		t.Error("tagged a non-event")
	}
}
