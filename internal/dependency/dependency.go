// Package dependency is the one call path from a delivery surface to listing,
// declaring and removing source dependence between episodes (ADR 0038,
// editing dependencies).
//
// The writes themselves are govwrite's: [govwrite.Writer.DeclareEventDependency]
// and [govwrite.Writer.RemoveEventDependency] on the daemon surfaces, and
// govwrite.ApplyDependencyDeclaration / ApplyDependencyRemoval inside the
// library's own governed kernel. What every surface also has to agree on is
// what happens before that call and how the result is rendered:
//
//   - ids are trimmed and required, and both episodes must exist; an episode
//     the caller's run scope does not cover reads exactly like one that does
//     not exist (retraction.CheckEvent), so a run-scoped token cannot probe
//     other runs;
//   - the kind is one of cites, copies, derived_from, and the note and the
//     removal reason are bounded;
//   - declaring a pair with a live dependency is [ErrExists] and removing a
//     pair without one is [ErrNotFound]. Both are checked here before the
//     governed write, and the store decides them again inside it: a
//     concurrent edit that lands in between comes back from the governed
//     write as the same sentinel (kernel.ExecuteWithCause keeps the
//     executor's error), so a race is a 409 / 404, never a 500.
//
// [Declare], [Remove] and [List] do those checks once for the CLI, MCP, REST,
// gRPC and the Go library, and [NewChangeReport] / [NewListReport] are the
// JSON wire forms the CLI, MCP and REST return byte for byte.
package dependency

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/retraction"
)

// ErrEventIDRequired is returned when the caller named no episode.
var ErrEventIDRequired = retraction.ErrEventIDRequired

// ErrEventNotFound is returned when either episode does not exist, or exists
// in a run the caller may not touch.
var ErrEventNotFound = retraction.ErrEventNotFound

// ErrInvalid is returned for a malformed request: no source id, a
// self-dependency, an unknown kind or an over-long note. REST answers 400 and
// gRPC InvalidArgument.
var ErrInvalid = errors.New("invalid episode dependency")

// ErrExists is returned when declaring a pair that already has a live
// dependency. REST answers 409 and gRPC AlreadyExists.
var ErrExists = domain.ErrDependencyExists

// ErrNotFound is returned when removing a pair that has no live dependency.
// REST answers 404 and gRPC NotFound.
var ErrNotFound = domain.ErrDependencyNotFound

// DeclareFunc performs the governed declaration.
type DeclareFunc func(ctx context.Context, dep domain.EventDependency) (domain.DependencyChange, error)

// RemoveFunc performs the governed removal.
type RemoveFunc func(ctx context.Context, eventID, dependsOn, actor, reason string) (domain.DependencyChange, error)

// DeclareRequest is one surface's declaration, already translated from its
// own input.
type DeclareRequest struct {
	// EventID is the dependent episode; DependsOn its source.
	EventID   string
	DependsOn string
	// Kind is cites, copies or derived_from.
	Kind string
	// Reference is an optional note saying why, stored as the dependency's
	// reference (at most domain.MaxDependencyReferenceLen characters).
	Reference string
	// Actor is recorded as who declared it. Empty records the system sentinel.
	Actor string
	// RunAllowed, when non-nil, confines the caller to episodes of the runs
	// it returns true for.
	RunAllowed func(runID string) bool
}

// RemoveRequest is one surface's removal.
type RemoveRequest struct {
	EventID   string
	DependsOn string
	// Reason is an optional note saying why, stored with the removal (at
	// most domain.MaxDependencyRemovalReasonLen characters).
	Reason     string
	Actor      string
	RunAllowed func(runID string) bool
}

// ListRequest is one surface's listing.
type ListRequest struct {
	EventID string
	// IncludeRemoved also returns removed dependencies, with who removed
	// them, when and why, and every earlier interval of a revived pair.
	IncludeRemoved bool
	RunAllowed     func(runID string) bool
}

// Declare validates req and performs the declaration.
func Declare(ctx context.Context, events ports.EventRepository, deps ports.EventDependencyRepository, declare DeclareFunc, req DeclareRequest) (domain.DependencyChange, error) {
	id, source, err := pair(req.EventID, req.DependsOn)
	if err != nil {
		return domain.DependencyChange{}, err
	}
	kind := domain.DependencyKind(strings.TrimSpace(req.Kind))
	switch kind {
	case domain.DependencyCites, domain.DependencyCopies, domain.DependencyDerivedFrom:
	default:
		return domain.DependencyChange{}, fmt.Errorf("%w: kind %q is not one of cites, copies, derived_from", ErrInvalid, req.Kind)
	}
	ref := strings.TrimSpace(req.Reference)
	if n := utf8.RuneCountInString(ref); n > domain.MaxDependencyReferenceLen {
		return domain.DependencyChange{}, fmt.Errorf("%w: reference is %d characters, at most %d", ErrInvalid, n, domain.MaxDependencyReferenceLen)
	}
	if events == nil || deps == nil || declare == nil {
		return domain.DependencyChange{}, errors.New("dependency: no event store, dependency store or declaration to run")
	}
	if err := checkBoth(ctx, events, id, source, req.RunAllowed); err != nil {
		return domain.DependencyChange{}, err
	}
	if live, err := liveRow(ctx, deps, id, source); err != nil {
		return domain.DependencyChange{}, err
	} else if live {
		return domain.DependencyChange{}, fmt.Errorf("%w: %s -> %s", ErrExists, id, source)
	}
	return declare(ctx, domain.EventDependency{
		EventID: id, DependsOn: source, Kind: kind, Signal: domain.SignalDeclared, Reference: ref, CreatedBy: req.Actor,
	})
}

// Remove validates req and performs the removal.
func Remove(ctx context.Context, events ports.EventRepository, deps ports.EventDependencyRepository, remove RemoveFunc, req RemoveRequest) (domain.DependencyChange, error) {
	id, source, err := pair(req.EventID, req.DependsOn)
	if err != nil {
		return domain.DependencyChange{}, err
	}
	reason := strings.TrimSpace(req.Reason)
	if n := utf8.RuneCountInString(reason); n > domain.MaxDependencyRemovalReasonLen {
		return domain.DependencyChange{}, fmt.Errorf("%w: reason is %d characters, at most %d", ErrInvalid, n, domain.MaxDependencyRemovalReasonLen)
	}
	if events == nil || deps == nil || remove == nil {
		return domain.DependencyChange{}, errors.New("dependency: no event store, dependency store or removal to run")
	}
	if err := checkBoth(ctx, events, id, source, req.RunAllowed); err != nil {
		return domain.DependencyChange{}, err
	}
	if live, err := liveRow(ctx, deps, id, source); err != nil {
		return domain.DependencyChange{}, err
	} else if !live {
		return domain.DependencyChange{}, fmt.Errorf("%w: %s -> %s", ErrNotFound, id, source)
	}
	return remove(ctx, id, source, req.Actor, reason)
}

// Listing is one episode's dependencies: what it depends on, what depends on
// it, and its citations that named no stored episode yet.
type Listing struct {
	EventID    string
	DependsOn  []domain.EventDependency
	Dependents []domain.EventDependency
	Pending    []domain.PendingReference
}

// List validates req and reads the episode's dependencies. A run-scoped
// caller sees only the rows whose other episode is in one of its runs.
func List(ctx context.Context, events ports.EventRepository, deps ports.EventDependencyRepository, req ListRequest) (Listing, error) {
	id := strings.TrimSpace(req.EventID)
	if id == "" {
		return Listing{}, ErrEventIDRequired
	}
	if events == nil || deps == nil {
		return Listing{}, errors.New("dependency: no event or dependency store to read")
	}
	if err := retraction.CheckEvent(ctx, events, id, req.RunAllowed); err != nil {
		return Listing{}, err
	}
	outgoing, incoming := deps.ListByEventIDs, deps.ListDependents
	if req.IncludeRemoved {
		outgoing, incoming = deps.ListByEventIDsWithRemoved, deps.ListDependentsWithRemoved
	}
	out, err := outgoing(ctx, []string{id})
	if err != nil {
		return Listing{}, fmt.Errorf("dependencies of %s: %w", id, err)
	}
	in, err := incoming(ctx, []string{id})
	if err != nil {
		return Listing{}, fmt.Errorf("dependents of %s: %w", id, err)
	}
	pending, err := deps.ListPendingByEventIDs(ctx, []string{id})
	if err != nil {
		return Listing{}, fmt.Errorf("pending citations of %s: %w", id, err)
	}
	if req.RunAllowed != nil {
		if out, err = withinRuns(ctx, events, out, func(d domain.EventDependency) string { return d.DependsOn }, req.RunAllowed); err != nil {
			return Listing{}, err
		}
		if in, err = withinRuns(ctx, events, in, func(d domain.EventDependency) string { return d.EventID }, req.RunAllowed); err != nil {
			return Listing{}, err
		}
	}
	sortDependencies(out)
	sortDependencies(in)
	return Listing{EventID: id, DependsOn: out, Dependents: in, Pending: pending}, nil
}

func pair(eventID, dependsOn string) (string, string, error) {
	id, source := strings.TrimSpace(eventID), strings.TrimSpace(dependsOn)
	if id == "" {
		return "", "", ErrEventIDRequired
	}
	if source == "" {
		return "", "", fmt.Errorf("%w: the source episode id (depends_on) is required", ErrInvalid)
	}
	if id == source {
		return "", "", fmt.Errorf("%w: episode %s cannot depend on itself", ErrInvalid, id)
	}
	return id, source, nil
}

func checkBoth(ctx context.Context, events ports.EventRepository, id, source string, runAllowed func(string) bool) error {
	if err := retraction.CheckEvent(ctx, events, id, runAllowed); err != nil {
		return err
	}
	return retraction.CheckEvent(ctx, events, source, runAllowed)
}

func liveRow(ctx context.Context, deps ports.EventDependencyRepository, id, source string) (bool, error) {
	rows, err := deps.ListByEventIDs(ctx, []string{id})
	if err != nil {
		return false, fmt.Errorf("dependencies of %s: %w", id, err)
	}
	for _, d := range rows {
		if d.DependsOn == source {
			return true, nil
		}
	}
	return false, nil
}

// withinRuns keeps the rows whose other episode (other(d)) is in an allowed run.
func withinRuns(ctx context.Context, events ports.EventRepository, rows []domain.EventDependency, other func(domain.EventDependency) string, allowed func(string) bool) ([]domain.EventDependency, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	ids := make([]string, 0, len(rows))
	for _, d := range rows {
		ids = append(ids, other(d))
	}
	found, err := events.ListByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("look up related episodes: %w", err)
	}
	ok := make(map[string]bool, len(found))
	for _, e := range found {
		ok[e.ID] = allowed(e.RunID)
	}
	kept := rows[:0]
	for _, d := range rows {
		if ok[other(d)] {
			kept = append(kept, d)
		}
	}
	return kept, nil
}

// sortDependencies is the one order every backend's rows are reported in.
func sortDependencies(rows []domain.EventDependency) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		if a.EventID != b.EventID {
			return a.EventID < b.EventID
		}
		return a.DependsOn < b.DependsOn
	})
}

// View is the JSON wire form of one dependency. Times are RFC 3339.
type View struct {
	EpisodeID     string `json:"episode_id"`
	DependsOn     string `json:"depends_on"`
	Kind          string `json:"kind"`
	Signal        string `json:"signal"`
	Reference     string `json:"reference"`
	CreatedAt     string `json:"created_at"`
	CreatedBy     string `json:"created_by"`
	RemovedAt     string `json:"removed_at,omitempty"`
	RemovedBy     string `json:"removed_by,omitempty"`
	RemovalReason string `json:"removal_reason,omitempty"`
}

// PendingView is the JSON wire form of a pending citation.
type PendingView struct {
	LookupKey string `json:"lookup_key"`
	CreatedAt string `json:"created_at"`
}

// ListReport is the JSON wire form of a listing, shared by the CLI's --json
// output, the MCP episode_dependencies tool and REST GET
// /v1/episodes/{id}/dependencies.
type ListReport struct {
	EpisodeID        string        `json:"episode_id"`
	DependsOn        []View        `json:"depends_on"`
	Dependents       []View        `json:"dependents"`
	PendingCitations []PendingView `json:"pending_citations"`
}

// ChangeReport is the JSON wire form of a declaration or removal, shared by
// the CLI, the MCP tools and REST.
type ChangeReport struct {
	Dependency      View     `json:"dependency"`
	RescoredBeliefs []string `json:"rescored_beliefs"`
	SourceRetracted bool     `json:"source_retracted"`
}

// NewView renders one dependency as its wire form.
func NewView(d domain.EventDependency) View {
	v := View{
		EpisodeID: d.EventID, DependsOn: d.DependsOn, Kind: string(d.Kind), Signal: string(d.Signal),
		Reference: d.Reference, CreatedAt: d.CreatedAt.UTC().Format(time.RFC3339Nano), CreatedBy: d.CreatedBy,
		RemovedBy: d.RemovedBy, RemovalReason: d.RemovalReason,
	}
	if d.RemovedAt != nil {
		v.RemovedAt = d.RemovedAt.UTC().Format(time.RFC3339Nano)
	}
	return v
}

// NewListReport renders a listing as its wire form.
func NewListReport(l Listing) ListReport {
	out := ListReport{EpisodeID: l.EventID, DependsOn: []View{}, Dependents: []View{}, PendingCitations: []PendingView{}}
	for _, d := range l.DependsOn {
		out.DependsOn = append(out.DependsOn, NewView(d))
	}
	for _, d := range l.Dependents {
		out.Dependents = append(out.Dependents, NewView(d))
	}
	for _, p := range l.Pending {
		out.PendingCitations = append(out.PendingCitations, PendingView{LookupKey: p.LookupKey, CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339Nano)})
	}
	return out
}

// NewChangeReport renders a declaration or removal as its wire form.
func NewChangeReport(c domain.DependencyChange) ChangeReport {
	rescored := c.RescoredBeliefs
	if rescored == nil {
		rescored = []string{}
	}
	return ChangeReport{Dependency: NewView(c.Dependency), RescoredBeliefs: rescored, SourceRetracted: c.SourceRetracted}
}
