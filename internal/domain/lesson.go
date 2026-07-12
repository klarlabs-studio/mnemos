package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Context narrows an entity (Belief, Schema, Reflex, Decision) to a
// specific operational context. Synthesis and query paths cluster
// and filter strictly within a context so a "rollback works for
// payments" record cannot quietly contaminate "rollback works for
// search". Empty context means "applies everywhere" — the small set
// of cross-cutting truths.
//
// Phase 8 adds Context (née Scope) to Claim/Belief and Decision; the
// existing LessonScope alias preserves callers that already used it.
type Context struct {
	Service string
	Env     string
	Team    string
}

// IsEmpty reports whether all context fields are unset.
func (s Context) IsEmpty() bool {
	return s.Service == "" && s.Env == "" && s.Team == ""
}

// Equal compares two contexts by value. Used by the cluster grouping
// pass so two actions with the same (service, env, team) sort into
// the same bucket regardless of map ordering.
func (s Context) Equal(o Context) bool {
	return s.Service == o.Service && s.Env == o.Env && s.Team == o.Team
}

// Key returns a stable string form for map indexing during synthesis.
func (s Context) Key() string {
	return s.Service + "|" + s.Env + "|" + s.Team
}

// Matches reports whether s satisfies the filter f. Empty fields in
// f are wildcards: a filter of {Service:"payments"} matches any context
// whose Service is "payments" regardless of Env/Team. Used by query
// paths that accept partial-context filters.
func (s Context) Matches(f Context) bool {
	if f.Service != "" && s.Service != f.Service {
		return false
	}
	if f.Env != "" && s.Env != f.Env {
		return false
	}
	if f.Team != "" && s.Team != f.Team {
		return false
	}
	return true
}

// Back-compat aliases (ADR 0011 Phase A) — remove at API v2.

// Scope is the pre-ADR-0011 name for Context; kept as a back-compat
// alias so existing call sites compile unchanged.
type Scope = Context

// LessonScope is the original Phase 3 name for Context (predating even
// Scope); kept as a back-compat alias.
type LessonScope = Context

// SchemaPolarity classifies a schema as a positive pattern to repeat
// or a negative pattern (anti-schema) to avoid.
type SchemaPolarity string

// Supported SchemaPolarity values.
const (
	// SchemaPolarityPositive marks clusters where the dominant outcome
	// is success — a pattern worth repeating.
	SchemaPolarityPositive SchemaPolarity = "positive"
	// SchemaPolarityNegative marks clusters where the dominant outcome
	// is failure — an anti-schema that warns operators away from a
	// known bad pattern.
	SchemaPolarityNegative SchemaPolarity = "negative"
)

// LessonPolarity is the pre-ADR-0011 name for SchemaPolarity; kept as
// a back-compat alias (remove at API v2).
type LessonPolarity = SchemaPolarity

// Back-compat aliases (ADR 0011 Phase A) — remove at API v2.
const (
	// LessonPolarityPositive is the pre-ADR-0011 name for SchemaPolarityPositive.
	LessonPolarityPositive = SchemaPolarityPositive
	// LessonPolarityNegative is the pre-ADR-0011 name for SchemaPolarityNegative.
	LessonPolarityNegative = SchemaPolarityNegative
)

// Schema is a validated operational truth derived from one or more
// Action -> Outcome chains. Schemas are the synthesis layer's output:
// they answer "what have we learned?" rather than "what do we
// believe?" (beliefs) or "what happened?" (episodes / actions).
//
// Evidence is the list of Action ids that corroborated the schema at
// derivation time. Confidence is in [0,1] and reflects corroboration
// count, outcome consistency, and recency — see internal/synthesize.
//
// LastVerified ticks forward when a fresh action+outcome pair re-
// confirms the schema, supporting the temporal-validity hardening of
// Phase 4 (decay-aware trust).
//
// Polarity is "positive" for success patterns and "negative" for
// anti-schemas derived from failure clusters. Empty is treated as
// positive for backward compatibility.
type Schema struct {
	ID           string
	Statement    string
	Scope        LessonScope
	Trigger      string   // optional — short label like "latency_spike_after_deploy" used for clustering and playbook/reflex lookup
	Kind         string   // optional free-form classifier (e.g. "rollback", "scale-up"), preserved verbatim
	Evidence     []string // Action ids that corroborated this schema
	Confidence   float64
	Polarity     SchemaPolarity // "positive" or "negative"; empty is treated as positive for backward compat
	DerivedAt    time.Time
	LastVerified time.Time
	Source       string // "synthesize" for engine-derived, "human" for hand-authored
	CreatedBy    string

	// SubjectClass classifies WHAT the schema is about — a specific instance
	// (individual) versus a category (class) — for the ADR 0012 promotion
	// eligibility gate. It flows up from the backing beliefs: a schema over
	// class-level beliefs is class-level (see AggregateSubjectClass). Empty
	// (SubjectClassUnknown) means unclassified. Only class-level schemas are
	// eligible to promote to the shared global brain.
	SubjectClass SubjectClass
}

// Lesson is the pre-ADR-0011 name for Schema; kept as a back-compat
// alias (remove at API v2).
type Lesson = Schema

// SchemaConfidenceMin is the floor below which the synthesis layer
// drops a candidate schema rather than emitting it. Tuned so a 3/3
// success cluster lands above the floor and a 2/3 success / 1/3
// contradiction cluster lands below.
const SchemaConfidenceMin = 0.55

// SchemaMinCorroboration is the smallest number of distinct
// corroborating actions required before the synthesis layer will
// emit a schema. Lower than 3 produces noisy folklore; higher slows
// the system's ability to learn from a thin corpus.
const SchemaMinCorroboration = 3

// Back-compat aliases (ADR 0011 Phase A) — remove at API v2.
const (
	// LessonConfidenceMin is the pre-ADR-0011 name for SchemaConfidenceMin.
	LessonConfidenceMin = SchemaConfidenceMin
	// LessonMinCorroboration is the pre-ADR-0011 name for SchemaMinCorroboration.
	LessonMinCorroboration = SchemaMinCorroboration
)

// Validate enforces minimum invariants for persistence. Empty scope
// is permitted (cross-cutting schemas) but evidence must have at
// least one entry — a schema without provenance is folklore.
func (l Schema) Validate() error {
	if strings.TrimSpace(l.ID) == "" {
		return errors.New("lesson id is required")
	}
	if strings.TrimSpace(l.Statement) == "" {
		return errors.New("lesson statement is required")
	}
	if l.Confidence < 0 || l.Confidence > 1 {
		return fmt.Errorf("lesson confidence must be in [0, 1], got %v", l.Confidence)
	}
	if len(l.Evidence) == 0 {
		return errors.New("lesson evidence must contain at least one action id")
	}
	for _, e := range l.Evidence {
		if strings.TrimSpace(e) == "" {
			return errors.New("lesson evidence entries must be non-empty")
		}
	}
	if l.DerivedAt.IsZero() {
		return errors.New("lesson derived_at is required")
	}
	return nil
}
