package mnemos

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/skills"
)

// ErrSkillsUnsupported is returned by the skill-record reads on a backend
// without the store asked for (actions, outcomes, lessons, playbooks, or the
// promoted global-schema store).
var ErrSkillsUnsupported = errors.New("mnemos: skill records not supported on this backend")

// Action is a recorded operational action, as read back by
// [Memory.ListActions].
type Action struct {
	ID        string
	RunID     string
	Kind      string
	Subject   string
	Actor     string
	At        time.Time
	Metadata  map[string]string
	CreatedBy string
	CreatedAt time.Time
}

// ActionQuery selects actions for [Memory.ListActions]. Subject and RunID are
// optional and narrow each other when both are set. Limit 0 means no cap.
type ActionQuery struct {
	Subject string
	RunID   string
	Limit   int
	Offset  int
}

// Outcome is the recorded result of an action, as read back by
// [Memory.ListOutcomes].
type Outcome struct {
	ID         string
	ActionID   string
	Result     string
	Metrics    map[string]float64
	Notes      string
	ObservedAt time.Time
	Source     string
	CreatedBy  string
	CreatedAt  time.Time
}

// OutcomeQuery selects outcomes for [Memory.ListOutcomes]. Limit 0 means no cap.
type OutcomeQuery struct {
	ActionID string
	Limit    int
	Offset   int
}

// Scope is the {Service, Env, Team} context a lesson, playbook or promoted
// schema applies to.
type Scope struct {
	Service string
	Env     string
	Team    string
}

// Lesson is a generalisation synthesised from action→outcome chains (a
// "schema" on the brain-native wire).
type Lesson struct {
	ID           string
	Statement    string
	Scope        Scope
	Trigger      string
	Kind         string
	Evidence     []string // action ids that corroborated the lesson
	Confidence   float64
	Polarity     string
	DerivedAt    time.Time
	LastVerified time.Time
	Source       string
	CreatedBy    string
}

// LessonQuery selects lessons for [Memory.ListLessons]. Limit 0 means no cap.
type LessonQuery struct {
	Service string
	Trigger string
	Limit   int
	Offset  int
}

// PlaybookStep is one ordered step of a [Playbook].
type PlaybookStep struct {
	Order       int
	Action      string
	Description string
	Condition   string
}

// Playbook is a trigger → steps response derived from lessons (a "reflex" on
// the brain-native wire). Mnemos returns the steps; executing them is the
// caller's.
type Playbook struct {
	ID                 string
	Trigger            string
	Statement          string
	Scope              Scope
	Steps              []PlaybookStep
	DerivedFromLessons []string
	Confidence         float64
	DerivedAt          time.Time
	LastVerified       time.Time
	Source             string
	CreatedBy          string
}

// PlaybookQuery selects playbooks for [Memory.ListPlaybooks]. Limit 0 means
// no cap.
type PlaybookQuery struct {
	Trigger string
	Service string
	Limit   int
	Offset  int
}

// GlobalSchema is a de-identified generalisation promoted to the shared
// global tier (ADR 0011/0012). Its provenance is corroboration counts, never
// tenant ids.
type GlobalSchema struct {
	ID              string
	Statement       string
	Scope           Scope
	Polarity        string
	DistinctTenants int
	EvidenceCount   int
	Confidence      float64
	Surprise        float64
	HasSurprise     bool
	Status          string // "pending" | "active"
	PromotedAt      time.Time
	CreatedBy       string
}

// GlobalSchemaQuery selects promoted schemas for [Memory.ListGlobalSchemas].
// Status is "", "pending" or "active". Limit 0 means no cap.
type GlobalSchemaQuery struct {
	Status string
	Limit  int
	Offset int
}

// ListActions implements [Memory.ListActions].
func (m *memory) ListActions(ctx context.Context, q ActionQuery) ([]Action, error) {
	got, err := skills.Actions(ctx, m.conn.Actions, skills.ActionFilter{Subject: q.Subject, RunID: q.RunID})
	if err != nil {
		return nil, skillErr("ListActions", err)
	}
	return mapSlice(skills.Window(got, q.Limit, q.Offset), toPublicAction), nil
}

// ListOutcomes implements [Memory.ListOutcomes].
func (m *memory) ListOutcomes(ctx context.Context, q OutcomeQuery) ([]Outcome, error) {
	got, err := skills.Outcomes(ctx, m.conn.Outcomes, m.conn.Actions, skills.OutcomeFilter{ActionID: q.ActionID})
	if err != nil {
		return nil, skillErr("ListOutcomes", err)
	}
	return mapSlice(skills.Window(got, q.Limit, q.Offset), toPublicOutcome), nil
}

// ListLessons implements [Memory.ListLessons].
func (m *memory) ListLessons(ctx context.Context, q LessonQuery) ([]Lesson, error) {
	got, err := skills.Schemas(ctx, m.conn.Lessons, skills.SchemaFilter{Service: q.Service, Trigger: q.Trigger})
	if err != nil {
		return nil, skillErr("ListLessons", err)
	}
	return mapSlice(skills.Window(got, q.Limit, q.Offset), toPublicLesson), nil
}

// ListPlaybooks implements [Memory.ListPlaybooks].
func (m *memory) ListPlaybooks(ctx context.Context, q PlaybookQuery) ([]Playbook, error) {
	got, err := skills.Reflexes(ctx, m.conn.Playbooks, skills.ReflexFilter{Trigger: q.Trigger, Service: q.Service})
	if err != nil {
		return nil, skillErr("ListPlaybooks", err)
	}
	return mapSlice(skills.Window(got, q.Limit, q.Offset), toPublicPlaybook), nil
}

// ListGlobalSchemas implements [Memory.ListGlobalSchemas].
func (m *memory) ListGlobalSchemas(ctx context.Context, q GlobalSchemaQuery) ([]GlobalSchema, error) {
	got, err := skills.GlobalSchemas(ctx, m.conn.GlobalSchemas, q.Status)
	if err != nil {
		return nil, skillErr("ListGlobalSchemas", err)
	}
	return mapSlice(skills.Window(got, q.Limit, q.Offset), toPublicGlobalSchema), nil
}

func skillErr(op string, err error) error {
	if errors.Is(err, skills.ErrUnsupported) {
		return fmt.Errorf("mnemos: %s: %w", op, ErrSkillsUnsupported)
	}
	return fmt.Errorf("mnemos: %s: %w", op, err)
}

func mapSlice[T, U any](xs []T, f func(T) U) []U {
	out := make([]U, 0, len(xs))
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}

func toPublicScope(s domain.Scope) Scope {
	return Scope{Service: s.Service, Env: s.Env, Team: s.Team}
}

func toPublicAction(a domain.Action) Action {
	return Action{
		ID: a.ID, RunID: a.RunID, Kind: string(a.Kind), Subject: a.Subject, Actor: a.Actor,
		At: a.At, Metadata: a.Metadata, CreatedBy: a.CreatedBy, CreatedAt: a.CreatedAt,
	}
}

func toPublicOutcome(o domain.Outcome) Outcome {
	return Outcome{
		ID: o.ID, ActionID: o.ActionID, Result: string(o.Result), Metrics: o.Metrics, Notes: o.Notes,
		ObservedAt: o.ObservedAt, Source: o.Source, CreatedBy: o.CreatedBy, CreatedAt: o.CreatedAt,
	}
}

func toPublicLesson(l domain.Lesson) Lesson {
	return Lesson{
		ID: l.ID, Statement: l.Statement, Scope: toPublicScope(l.Scope), Trigger: l.Trigger, Kind: l.Kind,
		Evidence: l.Evidence, Confidence: l.Confidence, Polarity: string(l.Polarity),
		DerivedAt: l.DerivedAt, LastVerified: l.LastVerified, Source: l.Source, CreatedBy: l.CreatedBy,
	}
}

func toPublicPlaybook(p domain.Playbook) Playbook {
	steps := make([]PlaybookStep, 0, len(p.Steps))
	for _, s := range p.Steps {
		steps = append(steps, PlaybookStep{Order: s.Order, Action: s.Action, Description: s.Description, Condition: s.Condition})
	}
	return Playbook{
		ID: p.ID, Trigger: p.Trigger, Statement: p.Statement, Scope: toPublicScope(p.Scope), Steps: steps,
		DerivedFromLessons: p.DerivedFromLessons, Confidence: p.Confidence,
		DerivedAt: p.DerivedAt, LastVerified: p.LastVerified, Source: p.Source, CreatedBy: p.CreatedBy,
	}
}

func toPublicGlobalSchema(s domain.GlobalSchema) GlobalSchema {
	return GlobalSchema{
		ID: s.ID, Statement: s.Statement, Scope: toPublicScope(s.Scope), Polarity: string(s.Polarity),
		DistinctTenants: s.DistinctTenants, EvidenceCount: s.EvidenceCount, Confidence: s.Confidence,
		Surprise: s.Surprise, HasSurprise: s.HasSurprise, Status: string(s.Status),
		PromotedAt: s.PromotedAt, CreatedBy: s.CreatedBy,
	}
}
