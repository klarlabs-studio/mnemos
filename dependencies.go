package mnemos

import (
	"context"
	"fmt"
	"time"

	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/mnemos/internal/dependency"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
)

// Editing source dependence between events on the library surface (ADR
// 0038). [Memory.EventDependencies], [Memory.DeclareEventDependency] and
// [Memory.RemoveEventDependency] run the same request checks as the CLI,
// MCP, REST and gRPC (internal/dependency) and the same writes
// (govwrite.ApplyDependencyDeclaration / ApplyDependencyRemoval), dispatched
// through the library's own governed kernel.

const (
	actionDeclareDependency = "declare_event_dependency"
	actionRemoveDependency  = "remove_event_dependency"
)

// ErrDependencyExists is returned by [Memory.DeclareEventDependency] when the
// pair already has a live dependency. Match it with errors.Is.
var ErrDependencyExists = dependency.ErrExists

// ErrDependencyNotFound is returned by [Memory.RemoveEventDependency] when
// the pair has no live dependency. Match it with errors.Is.
var ErrDependencyNotFound = dependency.ErrNotFound

// ErrInvalidDependency is returned for a malformed declaration or removal:
// no source id, an event depending on itself, an unknown kind or an over-long
// note. Match it with errors.Is.
var ErrInvalidDependency = dependency.ErrInvalid

// EventDependency is one recorded dependency: EventID cites, copies or derives
// from DependsOn. Kind is cites, copies or derived_from; Signal is what
// recorded it (metadata, link, named_source, or declared for one declared by
// hand); Reference is the text or note that justified it. RemovedAt is set on
// a dependency removed by hand, with RemovalReason the remover's reason, if
// any. A listing with removed dependencies has one entry per interval, so a
// pair removed and declared again appears once per time it was in force.
type EventDependency struct {
	EventID       string
	DependsOn     string
	Kind          string
	Signal        string
	Reference     string
	CreatedAt     time.Time
	CreatedBy     string
	RemovedAt     *time.Time
	RemovedBy     string
	RemovalReason string
}

// PendingCitation is a citation of an event that named no stored event yet;
// it resolves when an event carrying LookupKey is written.
type PendingCitation struct {
	LookupKey string
	CreatedAt time.Time
}

// EventDependencies is one event's source dependencies.
type EventDependencies struct {
	EventID string
	// DependsOn are the events it cites, copies or derives from.
	DependsOn []EventDependency
	// Dependents are the events that depend on it.
	Dependents       []EventDependency
	PendingCitations []PendingCitation
}

// EventDependencyChange is what a declaration or removal did: the stored
// dependency, the claims of the dependent event that were rescored, and
// whether the source event is retracted now. When it is, retracting it again
// would reach a different set of claims; the edit itself never retracts or
// un-retracts anything.
type EventDependencyChange struct {
	Dependency      EventDependency
	RescoredClaims  []string
	SourceRetracted bool
}

type declareDependencyInput struct{ Dependency domain.EventDependency }

type removeDependencyInput struct{ EventID, DependsOn, Actor, Reason string }

// EventDependencies implements [Memory.EventDependencies].
func (m *memory) EventDependencies(ctx context.Context, eventID string, includeRemoved bool) (EventDependencies, error) {
	l, err := dependency.List(ctx, m.conn.Events, m.conn.Dependencies, dependency.ListRequest{EventID: eventID, IncludeRemoved: includeRemoved})
	if err != nil {
		return EventDependencies{}, fmt.Errorf("mnemos: EventDependencies: %w", err)
	}
	out := EventDependencies{EventID: l.EventID, DependsOn: []EventDependency{}, Dependents: []EventDependency{}, PendingCitations: []PendingCitation{}}
	for _, d := range l.DependsOn {
		out.DependsOn = append(out.DependsOn, toPublicDependency(d))
	}
	for _, d := range l.Dependents {
		out.Dependents = append(out.Dependents, toPublicDependency(d))
	}
	for _, p := range l.Pending {
		out.PendingCitations = append(out.PendingCitations, PendingCitation{LookupKey: p.LookupKey, CreatedAt: p.CreatedAt})
	}
	return out, nil
}

// DeclareEventDependency implements [Memory.DeclareEventDependency].
func (m *memory) DeclareEventDependency(ctx context.Context, eventID, dependsOn, kind, note string) (EventDependencyChange, error) {
	declare := func(ctx context.Context, dep domain.EventDependency) (domain.DependencyChange, error) {
		return dispatchWrite[domain.DependencyChange](ctx, m, actionDeclareDependency, declareDependencyInput{Dependency: dep})
	}
	change, err := dependency.Declare(ctx, m.conn.Events, m.conn.Dependencies, declare, dependency.DeclareRequest{
		EventID: eventID, DependsOn: dependsOn, Kind: kind, Reference: note, Actor: m.actorID,
	})
	if err != nil {
		return EventDependencyChange{}, fmt.Errorf("mnemos: DeclareEventDependency: %w", err)
	}
	return toPublicDependencyChange(change), nil
}

// RemoveEventDependency implements [Memory.RemoveEventDependency].
func (m *memory) RemoveEventDependency(ctx context.Context, eventID, dependsOn, reason string) (EventDependencyChange, error) {
	remove := func(ctx context.Context, id, source, actor, reason string) (domain.DependencyChange, error) {
		return dispatchWrite[domain.DependencyChange](ctx, m, actionRemoveDependency, removeDependencyInput{EventID: id, DependsOn: source, Actor: actor, Reason: reason})
	}
	change, err := dependency.Remove(ctx, m.conn.Events, m.conn.Dependencies, remove, dependency.RemoveRequest{
		EventID: eventID, DependsOn: dependsOn, Reason: reason, Actor: m.actorID,
	})
	if err != nil {
		return EventDependencyChange{}, fmt.Errorf("mnemos: RemoveEventDependency: %w", err)
	}
	return toPublicDependencyChange(change), nil
}

type declareDependencyExecutor struct{ m *memory }

// Execute declares the dependency through the shared implementation and
// records it on the evidence chain.
func (e declareDependencyExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := writeInput[declareDependencyInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("%s: unexpected input type %T", actionDeclareDependency, input)
	}
	change, err := govwrite.ApplyDependencyDeclaration(ctx, e.m.conn, in.Dependency)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, err
	}
	return dependencyResult("declared", "mnemos.declare_event_dependency", change)
}

type removeDependencyExecutor struct{ m *memory }

// Execute removes the dependency through the shared implementation and
// records it on the evidence chain.
func (e removeDependencyExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := writeInput[removeDependencyInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("%s: unexpected input type %T", actionRemoveDependency, input)
	}
	change, err := govwrite.ApplyDependencyRemoval(ctx, e.m.conn, in.EventID, in.DependsOn, in.Actor, in.Reason)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, err
	}
	return dependencyResult("removed", "mnemos.remove_event_dependency", change)
}

func dependencyResult(verb, kind string, change domain.DependencyChange) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	d := change.Dependency
	actor := d.CreatedBy
	if d.RemovedAt != nil {
		actor = d.RemovedBy
	}
	return axidomain.ExecutionResult{
		Data: change,
		Summary: fmt.Sprintf("%s the dependency of event %s on event %s: %d claim(s) rescored (source retracted: %v)",
			verb, d.EventID, d.DependsOn, len(change.RescoredBeliefs), change.SourceRetracted),
	}, []axidomain.EvidenceRecord{{Kind: kind, Source: evidenceSourceLibrary, Value: map[string]any{
		"event_id": d.EventID, "depends_on": d.DependsOn, "kind": string(d.Kind), "signal": string(d.Signal),
		"rescored_claims": len(change.RescoredBeliefs), "source_retracted": change.SourceRetracted, "changed_by": actor,
		"reason": d.RemovalReason,
	}}}, nil
}

func toPublicDependency(d domain.EventDependency) EventDependency {
	return EventDependency{
		EventID: d.EventID, DependsOn: d.DependsOn, Kind: string(d.Kind), Signal: string(d.Signal), Reference: d.Reference,
		CreatedAt: d.CreatedAt, CreatedBy: d.CreatedBy, RemovedAt: d.RemovedAt, RemovedBy: d.RemovedBy, RemovalReason: d.RemovalReason,
	}
}

func toPublicDependencyChange(c domain.DependencyChange) EventDependencyChange {
	rescored := c.RescoredBeliefs
	if rescored == nil {
		rescored = []string{}
	}
	return EventDependencyChange{Dependency: toPublicDependency(c.Dependency), RescoredClaims: rescored, SourceRetracted: c.SourceRetracted}
}
