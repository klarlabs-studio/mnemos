package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Source dependence between episodes (ADR 0038).
//
// An episode depends on another when it says so: a citation, a link, or
// provenance metadata naming a stored source. A dependent episode is not
// independent evidence for what it repeats, so retraction (ADR 0037) follows
// dependencies as well as evidence links, and trust does not count a copy as
// a second voice.

// DependencyKind says how an episode depends on its source.
type DependencyKind string

const (
	// DependencyCites means the episode cites the source.
	DependencyCites DependencyKind = "cites"
	// DependencyCopies means the episode says it quotes, copies or reposts the source.
	DependencyCopies DependencyKind = "copies"
	// DependencyDerivedFrom means the episode says it is derived from, or based
	// on, the source.
	DependencyDerivedFrom DependencyKind = "derived_from"
)

// DependencySignal names the deterministic signal that recorded a dependency.
type DependencySignal string

const (
	// SignalMetadata means an explicit provenance metadata key (cites,
	// copied_from, derived_from) named the source.
	SignalMetadata DependencySignal = "metadata"
	// SignalLink means a URL in the text equals the source's identifier.
	SignalLink DependencySignal = "link"
	// SignalNamedSource means the phrase after a citation marker names the
	// source's source_input_id.
	SignalNamedSource DependencySignal = "named_source"
	// SignalDeclared means a person or agent declared the dependency by hand
	// (ADR 0038, editing dependencies); Reference holds their note.
	SignalDeclared DependencySignal = "declared"
)

// MaxDependencyReferenceLen is the longest note, in characters, a hand-declared
// dependency may carry: the same bound as a pending citation's lookup key.
const MaxDependencyReferenceLen = 512

// ErrDependencyExists is returned when a dependency is declared for a pair of
// episodes that already has a live one.
var ErrDependencyExists = errors.New("episode dependency already exists")

// ErrDependencyNotFound is returned when a dependency to remove is not stored
// or was already removed.
var ErrDependencyNotFound = errors.New("episode dependency not found")

// EventDependency records that EventID depends on DependsOn (ADR 0038).
// Rows are keyed by (EventID, DependsOn); the first recorded justification
// stands. A removed dependency is a tombstone (RemovedAt set), not a deleted
// row, so automatic detection cannot bring back what a person removed; only a
// declaration revives it.
type EventDependency struct {
	EventID   string           `json:"episode_id"`
	DependsOn string           `json:"depends_on"`
	Kind      DependencyKind   `json:"kind"`
	Signal    DependencySignal `json:"signal"`
	// Reference is the exact text or value that justified the dependency, so
	// a reader can check it.
	Reference string    `json:"reference"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
	// RemovedAt is when the dependency was removed, nil while it is live.
	RemovedAt *time.Time `json:"removed_at,omitempty"`
	// RemovedBy is who removed it, empty while it is live.
	RemovedBy string `json:"removed_by,omitempty"`
}

// Live reports whether the dependency has not been removed.
func (d EventDependency) Live() bool { return d.RemovedAt == nil }

// LiveAt reports whether the dependency was in force at t: recorded by t and
// not removed by then.
func (d EventDependency) LiveAt(t time.Time) bool {
	if d.CreatedAt.After(t) {
		return false
	}
	return d.RemovedAt == nil || d.RemovedAt.After(t)
}

// Validate checks that the dependency names two different episodes and a
// known kind and signal.
func (d EventDependency) Validate() error {
	if strings.TrimSpace(d.EventID) == "" || strings.TrimSpace(d.DependsOn) == "" {
		return errors.New("event dependency needs an episode id and the id it depends on")
	}
	if d.EventID == d.DependsOn {
		return fmt.Errorf("event %s cannot depend on itself", d.EventID)
	}
	switch d.Kind {
	case DependencyCites, DependencyCopies, DependencyDerivedFrom:
	default:
		return fmt.Errorf("event dependency kind %q is not one of cites, copies, derived_from", d.Kind)
	}
	switch d.Signal {
	case SignalMetadata, SignalLink, SignalNamedSource, SignalDeclared:
	default:
		return fmt.Errorf("event dependency signal %q is not one of metadata, link, named_source, declared", d.Signal)
	}
	return nil
}

// DependencyHop is one dependency on the path from a retracted episode to a
// dependent one, as the retraction report lists it.
type DependencyHop struct {
	EventID   string           `json:"episode_id"`
	DependsOn string           `json:"depends_on"`
	Kind      DependencyKind   `json:"kind"`
	Signal    DependencySignal `json:"signal"`
	Reference string           `json:"reference"`
}

// Hop is d as a report hop.
func (d EventDependency) Hop() DependencyHop {
	return DependencyHop{EventID: d.EventID, DependsOn: d.DependsOn, Kind: d.Kind, Signal: d.Signal, Reference: d.Reference}
}

// Belief outcomes a retraction reports a path for (ADR 0038 §5).
const (
	BeliefOutcomeRetracted = "retracted"
	BeliefOutcomeRescored  = "rescored"
	BeliefOutcomeKept      = "kept"
)

// BeliefPath is how a retraction reached a belief: Via is the episode chain
// from the retracted episode to the evidence that reached it ([S] for a
// belief linked to S directly).
type BeliefPath struct {
	BeliefID string   `json:"belief_id"`
	Outcome  string   `json:"outcome"`
	Via      []string `json:"via"`
}

// PendingReference is a citation that named no stored episode when its
// episode was written (ADR 0038, late citations). LookupKey is a canonical
// key a later source would carry (provenance.CanonicalKey); when an episode
// with that key is written, the citing episode's document is detected again
// and the dependency recorded then. Rows are append-only and keyed by
// (EventID, LookupKey); a resolved row is deleted.
type PendingReference struct {
	EventID   string    `json:"episode_id"`
	LookupKey string    `json:"lookup_key"`
	CreatedAt time.Time `json:"created_at"`
}

// Validate checks that the pending reference names an episode and a key.
func (p PendingReference) Validate() error {
	if strings.TrimSpace(p.EventID) == "" || strings.TrimSpace(p.LookupKey) == "" {
		return errors.New("pending reference needs an episode id and a lookup key")
	}
	return nil
}

// SortPendingReferences orders refs oldest first, then by episode and key:
// the one order every backend returns them in.
func SortPendingReferences(refs []PendingReference) {
	sort.Slice(refs, func(i, j int) bool {
		a, b := refs[i], refs[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		if a.EventID != b.EventID {
			return a.EventID < b.EventID
		}
		return a.LookupKey < b.LookupKey
	})
}

// DependencyChange is what declaring or removing a dependency did (ADR 0038,
// editing dependencies): the stored row, the beliefs evidenced by the
// dependent episode that were rescored, and whether the source episode is
// retracted now. Neither edit runs or undoes a retraction; SourceRetracted
// tells the caller that re-running retraction on the source would now reach a
// different set of beliefs.
type DependencyChange struct {
	Dependency      EventDependency `json:"dependency"`
	RescoredBeliefs []string        `json:"rescored_beliefs"`
	SourceRetracted bool            `json:"source_retracted"`
}
