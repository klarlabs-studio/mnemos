package markdown

import (
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestLesson_RoundTrip(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	l := domain.Lesson{
		ID:         "ls_xyz",
		Statement:  "Rollback recovers payments fast in prod",
		Trigger:    "latency_spike_after_deploy",
		Kind:       "rollback",
		Scope:      domain.Scope{Service: "payments", Env: "prod"},
		Evidence:   []string{"ac_1", "ac_2", "ac_3"},
		Confidence: 0.78,
		DerivedAt:  now,
		Source:     "synthesize",
	}
	out, err := ExportLesson(l)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.HasPrefix(out, "---\n") {
		t.Fatalf("want frontmatter, got: %q", out[:30])
	}
	doc, err := Parse(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.Kind != "lesson" || doc.Lesson == nil {
		t.Fatalf("expected lesson doc, got %+v", doc)
	}
	got := *doc.Lesson
	if got.ID != l.ID || got.Statement != l.Statement {
		t.Fatalf("id/statement mismatch: %+v", got)
	}
	if got.Trigger != l.Trigger || got.Kind != l.Kind {
		t.Fatalf("trigger/kind mismatch: %+v", got)
	}
	if got.Scope != l.Scope {
		t.Fatalf("scope mismatch: want %+v got %+v", l.Scope, got.Scope)
	}
	if len(got.Evidence) != 3 {
		t.Fatalf("evidence count: want 3 got %d", len(got.Evidence))
	}
	if got.Confidence != 0.78 {
		t.Fatalf("confidence: want 0.78 got %v", got.Confidence)
	}
}

func TestPlaybook_RoundTrip(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	p := domain.Playbook{
		ID:        "pb_abc",
		Trigger:   "latency_spike_after_deploy",
		Statement: "Respond to latency spike after deploy",
		Scope:     domain.Scope{Service: "payments"},
		Steps: []domain.PlaybookStep{
			{Order: 1, Action: "investigate", Description: "Confirm pattern"},
			{Order: 2, Action: "rollback", Description: "Roll back deploy"},
			{Order: 3, Action: "verify"},
		},
		DerivedFromLessons: []string{"ls_a", "ls_b"},
		Confidence:         0.82,
		DerivedAt:          now,
		Source:             "synthesize",
	}
	out, err := ExportPlaybook(p)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	doc, err := Parse(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.Kind != "playbook" || doc.Playbook == nil {
		t.Fatalf("expected playbook doc, got %+v", doc)
	}
	got := *doc.Playbook
	if got.ID != p.ID || got.Trigger != p.Trigger {
		t.Fatalf("id/trigger mismatch: %+v", got)
	}
	if len(got.Steps) != 3 || got.Steps[1].Action != "rollback" {
		t.Fatalf("steps mismatch: %+v", got.Steps)
	}
	if len(got.DerivedFromLessons) != 2 {
		t.Fatalf("lesson links: want 2 got %d", len(got.DerivedFromLessons))
	}
}

func TestParse_RejectsMissingFrontmatter(t *testing.T) {
	_, err := Parse("# just markdown\n")
	if err == nil {
		t.Fatal("expected error on missing frontmatter")
	}
}

func TestParse_HumanEditedDefaultsSource(t *testing.T) {
	doc, err := Parse(`---
id: ls_human
kind: lesson
trigger: x
scope:
  service: payments
---

# Statement

hand-authored
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.Lesson.Source != "human" {
		t.Fatalf("missing source should default to human, got %q", doc.Lesson.Source)
	}
}
