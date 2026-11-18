package provenance

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

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

// Detect returns the dependencies the written events declare (ADR 0038 §1).
// events are the episodes of one write, already stored. A citation anywhere
// in a document applies to every episode of that document (same
// source_input_id) in the write; a document never depends on itself. now and
// actor stamp the rows: a row is attributed to the citing episode's author,
// or to actor when the episode names none.
func Detect(ctx context.Context, lookup Lookup, events []domain.Event, now time.Time, actor string) ([]domain.EventDependency, error) {
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

	var out []domain.EventDependency
	seen := map[[2]string]bool{}
	for _, d := range docs {
		inDoc := make(map[string]bool, len(d.events))
		for _, ev := range d.events {
			inDoc[ev.ID] = true
		}
		var refs []Reference
		for _, ev := range d.events {
			refs = append(refs, References(ev)...)
		}
		for _, ref := range refs {
			targets, err := resolve(ctx, lookup, ref, d.source, inDoc)
			if err != nil {
				return nil, err
			}
			for _, target := range targets {
				for _, ev := range d.events {
					key := [2]string{ev.ID, target.ID}
					if seen[key] {
						continue
					}
					seen[key] = true
					out = append(out, domain.EventDependency{
						EventID: ev.ID, DependsOn: target.ID, Kind: ref.Kind, Signal: ref.Signal,
						Reference: ref.Text, CreatedAt: now, CreatedBy: authorOr(ev.CreatedBy, actor),
					})
				}
			}
		}
	}
	return out, nil
}

// resolve returns the stored episodes ref names, excluding the citing
// document's own.
func resolve(ctx context.Context, lookup Lookup, ref Reference, self string, inDoc map[string]bool) ([]domain.Event, error) {
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
		found, err := resolveExact(ctx, lookup, ref.Exact)
		if err != nil {
			return nil, err
		}
		return boundedBySource(foreign(found)), nil
	}
	// A named source: the longest run of the phrase's words that spells a
	// source_input_id wins; a run that spells two sources is ambiguous.
	for _, spellings := range nameRuns(ref.Phrase) {
		evs, err := lookup.ListBySourceInputIDs(ctx, spellings)
		if err != nil {
			return nil, fmt.Errorf("resolve named source %q: %w", ref.Phrase, err)
		}
		evs = foreign(evs)
		switch distinctSources(evs) {
		case 0:
			continue
		case 1:
			return boundedBySource(evs), nil
		default:
			return nil, nil
		}
	}
	// No run spells a source: an identifier written out whole (an episode id,
	// a path or a URL) may still name one exactly.
	found, err := resolveExact(ctx, lookup, rawTokens(ref.Phrase))
	if err != nil {
		return nil, err
	}
	return boundedBySource(foreign(found)), nil
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
