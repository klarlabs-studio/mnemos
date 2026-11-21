package govwrite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/mnemos/internal/clock"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/store"
)

// Editing source dependence between episodes (ADR 0038, editing
// dependencies).
//
// A person or agent can declare a dependency detection missed and remove one
// it got wrong. A removal is a tombstone on the row (removed_at, removed_by and
// an optional reason), never a delete: Append ignores a pair that is stored,
// so detection and late citation resolution cannot bring back what a person
// removed. Declaring a removed pair revives it with the new justification;
// the tombstone moves, unchanged, into the pair's edit history, so a
// recorded-as-of read still sees the dependency over the interval it was in
// force, and the removal itself stays on record.
//
// After either edit the beliefs the dependent episode evidences are rescored:
// trust counts a dependent evidence episode as a repeat, so only they can
// change. Neither edit runs or undoes a retraction. The result reports whether
// the source is retracted now, so the caller knows that retracting or
// un-retracting it again would reach a different set of beliefs.

const (
	actionDeclareDependency = "declare_event_dependency"
	actionRemoveDependency  = "remove_event_dependency"
)

type declareDependencyInput struct {
	Dependency domain.EventDependency
}

type removeDependencyInput struct {
	EventID   string
	DependsOn string
	Actor     string
	Reason    string
}

type declareDependencyExecutor struct{ conn *store.Conn }

// Execute declares the dependency and rescores the dependent episode's beliefs.
func (e declareDependencyExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := payload[declareDependencyInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("%s: unexpected input %T", actionDeclareDependency, input)
	}
	change, err := declareDependency(ctx, e.conn, in.Dependency, clock.Now().UTC())
	if err != nil {
		return axidomain.ExecutionResult{}, nil, err
	}
	d := change.Dependency
	return axidomain.ExecutionResult{
		Data: change,
		Summary: fmt.Sprintf("declared that episode %s %s episode %s: %d belief(s) rescored (source retracted: %v)",
			d.EventID, d.Kind, d.DependsOn, len(change.RescoredBeliefs), change.SourceRetracted),
	}, ev("mnemos.write.declare_event_dependency", map[string]any{
		"event_id": d.EventID, "depends_on": d.DependsOn, "kind": string(d.Kind), "reference": d.Reference,
		"rescored_beliefs": len(change.RescoredBeliefs), "source_retracted": change.SourceRetracted,
		"changed_by": d.CreatedBy,
	}), nil
}

type removeDependencyExecutor struct{ conn *store.Conn }

// Execute removes the dependency and rescores the dependent episode's beliefs.
func (e removeDependencyExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := payload[removeDependencyInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("%s: unexpected input %T", actionRemoveDependency, input)
	}
	change, err := removeDependency(ctx, e.conn, in.EventID, in.DependsOn, in.Actor, in.Reason, clock.Now().UTC())
	if err != nil {
		return axidomain.ExecutionResult{}, nil, err
	}
	d := change.Dependency
	return axidomain.ExecutionResult{
		Data: change,
		Summary: fmt.Sprintf("removed the dependency of episode %s on episode %s: %d belief(s) rescored (source retracted: %v)",
			d.EventID, d.DependsOn, len(change.RescoredBeliefs), change.SourceRetracted),
	}, ev("mnemos.write.remove_event_dependency", map[string]any{
		"event_id": d.EventID, "depends_on": d.DependsOn, "kind": string(d.Kind), "signal": string(d.Signal),
		"rescored_beliefs": len(change.RescoredBeliefs), "source_retracted": change.SourceRetracted,
		"changed_by": d.RemovedBy, "reason": d.RemovalReason,
	}), nil
}

// DeclareEventDependency declares that dep.EventID depends on dep.DependsOn
// (ADR 0038, editing dependencies) and rescores the dependent episode's
// beliefs. Signal and CreatedAt are set here; CreatedBy is the actor. A live
// dependency for the pair is an error wrapping domain.ErrDependencyExists,
// through the kernel as well as directly (ApplyDependencyDeclaration): the
// dispatch keeps the executor's error (kernel.ExecuteWithCause).
func (w *Writer) DeclareEventDependency(ctx context.Context, dep domain.EventDependency) (domain.DependencyChange, error) {
	return dispatch[domain.DependencyChange](ctx, w, actionDeclareDependency, declareDependencyInput{Dependency: dep})
}

// RemoveEventDependency marks the (eventID, dependsOn) dependency removed by
// actor, for reason (may be empty), and rescores the dependent episode's
// beliefs. A pair with no live dependency is an error wrapping
// domain.ErrDependencyNotFound.
func (w *Writer) RemoveEventDependency(ctx context.Context, eventID, dependsOn, actor, reason string) (domain.DependencyChange, error) {
	return dispatch[domain.DependencyChange](ctx, w, actionRemoveDependency, removeDependencyInput{
		EventID: strings.TrimSpace(eventID), DependsOn: strings.TrimSpace(dependsOn), Actor: actor, Reason: strings.TrimSpace(reason),
	})
}

// ApplyDependencyDeclaration declares the dependency directly against conn,
// outside this package's kernel, for the root library's own governed kernel
// (see [ApplyRetraction]). Every other caller goes through
// [Writer.DeclareEventDependency].
func ApplyDependencyDeclaration(ctx context.Context, conn *store.Conn, dep domain.EventDependency) (domain.DependencyChange, error) {
	return declareDependency(ctx, conn, dep, clock.Now().UTC())
}

// ApplyDependencyRemoval removes the dependency directly against conn, for
// the root library's own governed kernel. Every other caller goes through
// [Writer.RemoveEventDependency].
func ApplyDependencyRemoval(ctx context.Context, conn *store.Conn, eventID, dependsOn, actor, reason string) (domain.DependencyChange, error) {
	return removeDependency(ctx, conn, strings.TrimSpace(eventID), strings.TrimSpace(dependsOn), actor, strings.TrimSpace(reason), clock.Now().UTC())
}

var errNoDependencyStore = errors.New("this backend has no episode dependency store")

func declareDependency(ctx context.Context, conn *store.Conn, dep domain.EventDependency, now time.Time) (domain.DependencyChange, error) {
	if conn == nil || conn.Dependencies == nil {
		return domain.DependencyChange{}, errNoDependencyStore
	}
	dep.EventID, dep.DependsOn = strings.TrimSpace(dep.EventID), strings.TrimSpace(dep.DependsOn)
	dep.Signal, dep.CreatedAt, dep.CreatedBy = domain.SignalDeclared, now, actorOr(dep.CreatedBy)
	dep.RemovedAt, dep.RemovedBy, dep.RemovalReason = nil, "", ""
	stored, err := conn.Dependencies.Declare(ctx, dep)
	if err != nil {
		return domain.DependencyChange{}, fmt.Errorf("declare dependency %s -> %s: %w", dep.EventID, dep.DependsOn, err)
	}
	return afterDependencyEdit(ctx, conn, stored)
}

func removeDependency(ctx context.Context, conn *store.Conn, eventID, dependsOn, actor, reason string, now time.Time) (domain.DependencyChange, error) {
	if conn == nil || conn.Dependencies == nil {
		return domain.DependencyChange{}, errNoDependencyStore
	}
	stored, err := conn.Dependencies.Remove(ctx, eventID, dependsOn, actorOr(actor), reason, now)
	if err != nil {
		return domain.DependencyChange{}, fmt.Errorf("remove dependency %s -> %s: %w", eventID, dependsOn, err)
	}
	return afterDependencyEdit(ctx, conn, stored)
}

// afterDependencyEdit rescores the dependent episode's beliefs and reads the
// source's retraction state. It does not retract or un-retract anything.
func afterDependencyEdit(ctx context.Context, conn *store.Conn, stored domain.EventDependency) (domain.DependencyChange, error) {
	rescored, err := pipeline.RescoreBeliefsOfEpisodes(ctx, conn, []string{stored.EventID})
	if err != nil {
		return domain.DependencyChange{}, fmt.Errorf("rescore after dependency edit: %w", err)
	}
	_, retracted, err := LookupRetraction(ctx, conn, stored.DependsOn)
	if err != nil {
		return domain.DependencyChange{}, fmt.Errorf("retraction state of %s: %w", stored.DependsOn, err)
	}
	return domain.DependencyChange{Dependency: stored, RescoredBeliefs: rescored, SourceRetracted: retracted}, nil
}
