package provenance

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.klarlabs.de/mnemos/internal/domain"
)

// MaxEpisodesPerSource bounds how many episodes one citation may make an
// episode depend on. A source with more is not a specific citation (an
// identifier shared by a whole capture stream, say), and nothing is recorded.
const MaxEpisodesPerSource = 1000

// Lookup is the read side Detect resolves references against: the event
// repository plus ports.EventSourceLister.
type Lookup interface {
	ListByIDs(ctx context.Context, ids []string) ([]domain.Event, error)
	ListBySourceInputIDs(ctx context.Context, ids []string) ([]domain.Event, error)
	ListBySourcePath(ctx context.Context, path string) ([]domain.Event, error)
}

// Detection is what [DetectAll] found in one write: the dependencies to
// record, and the references that named no stored episode yet.
type Detection struct {
	Dependencies []domain.EventDependency
	// Pending are the lookup keys of every reference that resolved to no
	// stored episode, stamped with the episode that made the reference. A
	// reference that is ambiguous (its phrase spells two sources) is never
	// pending: no later source can make it specific.
	Pending []domain.PendingReference
}

// Detect returns the dependencies the written events declare (ADR 0038 §1).
// It is [DetectAll] without the pending references.
func Detect(ctx context.Context, lookup Lookup, events []domain.Event, now time.Time, actor string) ([]domain.EventDependency, error) {
	d, err := DetectAll(ctx, lookup, events, now, actor)
	return d.Dependencies, err
}

// DetectAll returns the dependencies the written events declare (ADR 0038 §1)
// and the references that resolved to nothing yet (late citations). events
// are the episodes of one write, already stored. A citation anywhere in a
// document applies to every episode of that document (same source_input_id)
// in the write; a document never depends on itself, and none of its own keys
// is ever pending. now and actor stamp the rows: a dependency is attributed
// to the citing episode's author, or to actor when the episode names none.
func DetectAll(ctx context.Context, lookup Lookup, events []domain.Event, now time.Time, actor string) (Detection, error) {
	type document struct {
		source string
		events []domain.Event
	}
	var docs []*document
	bySource := map[string]*document{}
	for _, ev := range events {
		d := bySource[ev.SourceInputID]
		if d == nil {
			d = &document{source: ev.SourceInputID}
			bySource[ev.SourceInputID] = d
			docs = append(docs, d)
		}
		d.events = append(d.events, ev)
	}

	var out Detection
	seen := map[[2]string]bool{}
	pendingSeen := map[[2]string]bool{}
	pendingCount := map[string]int{}
	for _, d := range docs {
		inDoc := make(map[string]bool, len(d.events))
		own := map[string]bool{}
		for _, ev := range d.events {
			inDoc[ev.ID] = true
			for _, k := range EpisodeKeys(ev) {
				own[k] = true
			}
		}
		for _, citing := range d.events {
			for _, ref := range References(citing) {
				r, err := resolve(ctx, lookup, ref, d.source, inDoc)
				if err != nil {
					return Detection{}, err
				}
				for _, target := range r.targets {
					for _, ev := range d.events {
						key := [2]string{ev.ID, target.ID}
						if seen[key] {
							continue
						}
						seen[key] = true
						out.Dependencies = append(out.Dependencies, domain.EventDependency{
							EventID: ev.ID, DependsOn: target.ID, Kind: ref.Kind, Signal: ref.Signal,
							Reference: ref.Text, CreatedAt: now, CreatedBy: authorOr(ev.CreatedBy, actor),
						})
					}
				}
				for _, k := range r.pending {
					key := [2]string{citing.ID, k}
					if own[k] || pendingSeen[key] || utf8.RuneCountInString(k) > MaxLookupKeyLen ||
						pendingCount[citing.ID] >= MaxPendingKeysPerEpisode {
						continue
					}
					pendingSeen[key] = true
					pendingCount[citing.ID]++
					out.Pending = append(out.Pending, domain.PendingReference{EventID: citing.ID, LookupKey: k, CreatedAt: now})
				}
			}
		}
	}
	return out, nil
}

// resolution is what one reference resolved to: the stored episodes it names
// (excluding the citing document's own), or, for what it could not resolve,
// the lookup keys under which a later source would resolve it.
type resolution struct {
	targets []domain.Event
	pending []string
}

// resolve resolves ref against the stored episodes, excluding the citing
// document's own.
func resolve(ctx context.Context, lookup Lookup, ref Reference, self string, inDoc map[string]bool) (resolution, error) {
	foreign := func(evs []domain.Event) []domain.Event {
		out := evs[:0:0]
		for _, e := range evs {
			if e.SourceInputID != self && !inDoc[e.ID] {
				out = append(out, e)
			}
		}
		return out
	}
	if ref.Signal != domain.SignalNamedSource {
		// Each identifier resolves on its own, so one stored id among several
		// does not hide the others that are not stored yet. An identifier that
		// matches anything, even the citing document itself, is not pending.
		var (
			r     resolution
			found []domain.Event
		)
		for _, id := range ref.Exact {
			evs, err := resolveExact(ctx, lookup, []string{id})
			if err != nil {
				return resolution{}, err
			}
			if len(evs) == 0 {
				if k := CanonicalKey(id); k != "" {
					r.pending = append(r.pending, k)
				}
				continue
			}
			found = append(found, evs...)
		}
		r.targets = boundedBySource(foreign(found))
		return r, nil
	}
	// A named source: the longest run of the phrase's words that spells a
	// source_input_id wins; a run that spells two sources is ambiguous, and an
	// ambiguous reference is never pending.
	for _, spellings := range nameRuns(ref.Phrase) {
		evs, err := lookup.ListBySourceInputIDs(ctx, spellings)
		if err != nil {
			return resolution{}, fmt.Errorf("resolve named source %q: %w", ref.Phrase, err)
		}
		evs = foreign(evs)
		switch distinctSources(evs) {
		case 0:
			continue
		case 1:
			return resolution{targets: boundedBySource(evs)}, nil
		default:
			return resolution{}, nil
		}
	}
	// No run spells a source: an identifier written out whole (an episode id,
	// a path or a URL) may still name one exactly.
	found, err := resolveExact(ctx, lookup, rawTokens(ref.Phrase))
	if err != nil {
		return resolution{}, err
	}
	if targets := foreign(found); len(targets) > 0 {
		return resolution{targets: boundedBySource(targets)}, nil
	}
	return resolution{pending: phraseKeys(ref.Phrase)}, nil
}

// resolveExact returns the episodes whose id, source_input_id or
// input_source_path equals one of ids.
func resolveExact(ctx context.Context, lookup Lookup, ids []string) ([]domain.Event, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	byID, err := lookup.ListByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("resolve episode ids: %w", err)
	}
	bySource, err := lookup.ListBySourceInputIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("resolve source ids: %w", err)
	}
	out := make([]domain.Event, 0, len(byID)+len(bySource))
	out = append(append(out, byID...), bySource...)
	for _, id := range ids {
		if !strings.Contains(id, "/") {
			continue
		}
		byPath, err := lookup.ListBySourcePath(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("resolve source path: %w", err)
		}
		out = append(out, byPath...)
	}
	return dedupe(out), nil
}

func authorOr(author, fallback string) string {
	if strings.TrimSpace(author) != "" {
		return author
	}
	return fallback
}

func distinctSources(evs []domain.Event) int {
	seen := map[string]bool{}
	for _, e := range evs {
		seen[e.SourceInputID] = true
	}
	return len(seen)
}

// boundedBySource drops every source with more than MaxEpisodesPerSource
// episodes in evs, and returns the rest sorted by id.
func boundedBySource(evs []domain.Event) []domain.Event {
	evs = dedupe(evs)
	count := map[string]int{}
	for _, e := range evs {
		count[e.SourceInputID]++
	}
	out := evs[:0:0]
	for _, e := range evs {
		if count[e.SourceInputID] <= MaxEpisodesPerSource {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func dedupe(evs []domain.Event) []domain.Event {
	seen := map[string]bool{}
	out := evs[:0:0]
	for _, e := range evs {
		if !seen[e.ID] {
			seen[e.ID] = true
			out = append(out, e)
		}
	}
	return out
}
