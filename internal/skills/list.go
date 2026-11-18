// Package skills is the one implementation of reading and appending the
// skill-loop records every transport exposes: actions, outcomes, schemas
// (lessons), reflexes (playbooks) and the schemas promoted to the shared
// global tier.
//
// Before it, gRPC answered these from inline repository calls and the other
// transports either re-derived the same filter precedence or had no listing at
// all. Each surface now translates its own request into a filter, calls the
// function here, and pages the result with [Page] and [Window], so the same
// question gets the same records on the Go library, REST, gRPC, MCP and the
// client.
package skills

import (
	"context"
	"errors"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// ErrUnsupported is returned when the backend has no store for the records
// asked for.
var ErrUnsupported = errors.New("not supported on this backend")

// ErrRunNotAllowed is returned when a filter names a run the caller's run
// scope does not cover. Naming a forbidden run is refused rather than
// answered with an empty page: a caller that cannot tell "no actions" from
// "not yours" will read the first as fact.
var ErrRunNotAllowed = errors.New("run not in the caller's run allowlist")

// ErrInvalidFilter is returned for a filter value outside its vocabulary.
var ErrInvalidFilter = errors.New("invalid filter")

// Paging defaults shared by every transport that pages a listing.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Page normalises a requested limit and offset: a non-positive limit is
// DefaultLimit, a limit above MaxLimit is MaxLimit, a negative offset is 0.
func Page(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// Window returns xs[offset:offset+limit], clamped to the slice. A
// non-positive limit means no cap.
func Window[T any](xs []T, limit, offset int) []T {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(xs) {
		return nil
	}
	end := len(xs)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return xs[offset:end]
}

// RunScope is a caller's run allowlist as a predicate. Nil allows every run.
type RunScope func(runID string) bool

func (s RunScope) allows(runID string) bool { return s == nil || s(runID) }

// ActionFilter selects actions. Subject wins over RunID when both are set;
// the other is then applied as a narrowing filter.
type ActionFilter struct {
	Subject string
	RunID   string
	// Runs confines the result to actions of the runs it allows. Actions
	// with no run are outside every restricted scope.
	Runs RunScope
}

// Actions lists recorded actions.
func Actions(ctx context.Context, repo ports.ActionRepository, f ActionFilter) ([]domain.Action, error) {
	if repo == nil {
		return nil, fmt.Errorf("list actions: %w", ErrUnsupported)
	}
	if f.RunID != "" && !f.Runs.allows(f.RunID) {
		return nil, fmt.Errorf("run_id %q: %w", f.RunID, ErrRunNotAllowed)
	}
	var (
		actions []domain.Action
		err     error
	)
	switch {
	case f.Subject != "":
		actions, err = repo.ListBySubject(ctx, f.Subject)
	case f.RunID != "":
		actions, err = repo.ListByRunID(ctx, f.RunID)
	default:
		actions, err = repo.ListAll(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list actions: %w", err)
	}
	return keep(actions, func(a domain.Action) bool {
		if f.RunID != "" && a.RunID != f.RunID {
			return false
		}
		return f.Runs.allows(a.RunID)
	}), nil
}

// OutcomeFilter selects outcomes.
type OutcomeFilter struct {
	ActionID string
	// Runs confines the result to outcomes of actions in the runs it allows.
	// An outcome carries no run of its own; it belongs to its action's.
	Runs RunScope
}

// Outcomes lists recorded outcomes. actions is consulted only when f.Runs
// restricts the caller, to resolve each outcome's action to its run.
func Outcomes(ctx context.Context, repo ports.OutcomeRepository, actions ports.ActionRepository, f OutcomeFilter) ([]domain.Outcome, error) {
	if repo == nil {
		return nil, fmt.Errorf("list outcomes: %w", ErrUnsupported)
	}
	var (
		outcomes []domain.Outcome
		err      error
	)
	if f.ActionID != "" {
		outcomes, err = repo.ListByActionID(ctx, f.ActionID)
	} else {
		outcomes, err = repo.ListAll(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list outcomes: %w", err)
	}
	if f.Runs == nil {
		return outcomes, nil
	}
	if actions == nil {
		return nil, fmt.Errorf("list outcomes: resolve action runs: %w", ErrUnsupported)
	}
	all, err := actions.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list outcomes: resolve action runs: %w", err)
	}
	allowed := make(map[string]bool, len(all))
	for _, a := range all {
		if f.Runs.allows(a.RunID) {
			allowed[a.ID] = true
		}
	}
	return keep(outcomes, func(o domain.Outcome) bool { return allowed[o.ActionID] }), nil
}

// SchemaFilter selects schemas (lessons). Service wins over Trigger when both
// are set; the other is then applied as a narrowing filter.
type SchemaFilter struct {
	Service string
	Trigger string
}

// Schemas lists the tenant's synthesized or appended schemas (lessons).
func Schemas(ctx context.Context, repo ports.LessonRepository, f SchemaFilter) ([]domain.Lesson, error) {
	if repo == nil {
		return nil, fmt.Errorf("list schemas: %w", ErrUnsupported)
	}
	var (
		lessons []domain.Lesson
		err     error
	)
	switch {
	case f.Service != "":
		lessons, err = repo.ListByService(ctx, f.Service)
	case f.Trigger != "":
		lessons, err = repo.ListByTrigger(ctx, f.Trigger)
	default:
		lessons, err = repo.ListAll(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list schemas: %w", err)
	}
	return keep(lessons, func(l domain.Lesson) bool {
		return (f.Service == "" || l.Scope.Service == f.Service) && (f.Trigger == "" || l.Trigger == f.Trigger)
	}), nil
}

// ReflexFilter selects reflexes (playbooks). Trigger wins over Service when
// both are set; the other is then applied as a narrowing filter.
type ReflexFilter struct {
	Trigger string
	Service string
}

// Reflexes lists the tenant's reflexes (playbooks).
func Reflexes(ctx context.Context, repo ports.PlaybookRepository, f ReflexFilter) ([]domain.Playbook, error) {
	if repo == nil {
		return nil, fmt.Errorf("list reflexes: %w", ErrUnsupported)
	}
	var (
		playbooks []domain.Playbook
		err       error
	)
	switch {
	case f.Trigger != "":
		playbooks, err = repo.ListByTrigger(ctx, f.Trigger)
	case f.Service != "":
		playbooks, err = repo.ListByService(ctx, f.Service)
	default:
		playbooks, err = repo.ListAll(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list reflexes: %w", err)
	}
	return keep(playbooks, func(p domain.Playbook) bool {
		return (f.Trigger == "" || p.Trigger == f.Trigger) && (f.Service == "" || p.Scope.Service == f.Service)
	}), nil
}

// GlobalSchemas lists the schemas promoted to the shared global tier, all of
// them or only those with the given status (pending | active). The promoted
// store is deliberately cross-tenant (ADR 0011/0012): callers pass the base
// connection's repository, never a tenant-scoped one.
func GlobalSchemas(ctx context.Context, repo ports.GlobalSchemaRepository, status string) ([]domain.GlobalSchema, error) {
	if repo == nil {
		return nil, fmt.Errorf("list global schemas: promoted-schema store: %w", ErrUnsupported)
	}
	switch status {
	case "":
		out, err := repo.ListAll(ctx)
		if err != nil {
			return nil, fmt.Errorf("list global schemas: %w", err)
		}
		return out, nil
	case string(domain.GlobalSchemaStatusPending), string(domain.GlobalSchemaStatusActive):
		out, err := repo.ListByStatus(ctx, domain.GlobalSchemaStatus(status))
		if err != nil {
			return nil, fmt.Errorf("list global schemas: %w", err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("status must be pending or active, got %q: %w", status, ErrInvalidFilter)
	}
}

func keep[T any](xs []T, ok func(T) bool) []T {
	out := xs[:0:0]
	for _, x := range xs {
		if ok(x) {
			out = append(out, x)
		}
	}
	return out
}
