package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ReflexStep is one ordered instruction in a Reflex. Steps are
// the contract Praxis (the execution layer) consumes; Mnemos returns
// them and steps aside for execution. Condition is optional and free-
// form — Praxis interprets it; Mnemos preserves the string.
type ReflexStep struct {
	Order       int    // 1-indexed position in the reflex
	Action      string // short imperative verb (e.g. "rollback", "scale_up", "page_oncall")
	Description string // human-readable expansion
	Condition   string // optional precondition or guard, free-form
}

// PlaybookStep is the pre-ADR-0011 name for ReflexStep; kept as a
// back-compat alias (remove at API v2).
type PlaybookStep = ReflexStep

// Validate enforces minimum invariants on a step.
func (s ReflexStep) Validate() error {
	if s.Order < 1 {
		return fmt.Errorf("playbook step order must be >= 1, got %d", s.Order)
	}
	if strings.TrimSpace(s.Action) == "" {
		return errors.New("playbook step action is required")
	}
	return nil
}

// Reflex is structured operational intelligence: a trigger pattern
// (the situation the reflex responds to) plus an ordered list of
// steps. Mnemos returns reflexes; Praxis executes them. The
// separation is load-bearing — Mnemos cannot mutate the world, so
// reflexes must be steps-only.
//
// Reflexes come from two paths: synthesis (derived from clustered
// Schemas) and human authoring. Source distinguishes the two so the
// trust formula can weight them differently. DerivedFromLessons
// preserves provenance back to the schemas that justified the
// synthesised reflex.
type Reflex struct {
	ID                 string
	Trigger            string // canonical trigger label, e.g. "latency_spike_after_deploy"
	Statement          string // short summary of what this reflex does
	Scope              LessonScope
	Steps              []ReflexStep
	DerivedFromLessons []string // schema (lesson) ids
	Confidence         float64  // 0..1
	DerivedAt          time.Time
	LastVerified       time.Time
	Source             string // "synthesize" | "human"
	CreatedBy          string
}

// Playbook is the pre-ADR-0011 name for Reflex; kept as a back-compat
// alias (remove at API v2).
type Playbook = Reflex

// ReflexConfidenceMin is the floor below which the synthesis layer
// drops a candidate reflex. Tuned so a single high-confidence
// schema can seed a reflex (a one-schema cluster scores ~0.55 with
// SchemaConfidenceMin=0.55), while two thin schemas cannot.
const ReflexConfidenceMin = 0.55

// ReflexMinLessons is the smallest number of corroborating schemas
// the synthesis layer requires before emitting a synthesised
// reflex. One is enough — a single high-confidence schema with
// recurring evidence is a credible reflex seed; the schema layer
// already filters for corroboration upstream.
const ReflexMinLessons = 1

// Back-compat aliases (ADR 0011 Phase A) — remove at API v2.
const (
	// PlaybookConfidenceMin is the pre-ADR-0011 name for ReflexConfidenceMin.
	PlaybookConfidenceMin = ReflexConfidenceMin
	// PlaybookMinLessons is the pre-ADR-0011 name for ReflexMinLessons.
	PlaybookMinLessons = ReflexMinLessons
)

// Validate enforces minimum invariants for persistence. Steps may be
// empty for placeholder/in-progress reflexes; ID, Trigger,
// Statement, Confidence, and DerivedAt are required.
func (p Reflex) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return errors.New("playbook id is required")
	}
	if strings.TrimSpace(p.Trigger) == "" {
		return errors.New("playbook trigger is required")
	}
	if strings.TrimSpace(p.Statement) == "" {
		return errors.New("playbook statement is required")
	}
	if p.Confidence < 0 || p.Confidence > 1 {
		return fmt.Errorf("playbook confidence must be in [0, 1], got %v", p.Confidence)
	}
	if p.DerivedAt.IsZero() {
		return errors.New("playbook derived_at is required")
	}
	for i, s := range p.Steps {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("playbook step %d: %w", i, err)
		}
	}
	return nil
}
