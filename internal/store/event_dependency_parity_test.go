package store_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// ADR 0038: source dependence between episodes is stored, read and destroyed
// the same way on every backend. Asserted on what each store returns.
func TestEventDependencies_AgreeAcrossBackends(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 18, 9, 30, 15, 123456000, time.UTC)
	for _, b := range openBackends(t) {
		deps := b.conn.Dependencies
		if deps == nil {
			t.Fatalf("%s: Conn.Dependencies is nil", b.name)
		}
		for _, e := range []domain.Event{
			{ID: "dep-a", Content: "advisory", SourceInputID: "vendor-advisory", SchemaVersion: "v1", Timestamp: at, IngestedAt: at},
			{ID: "dep-b", Content: "blog copy", SourceInputID: "security-blog", SchemaVersion: "v1", Timestamp: at, IngestedAt: at},
			{ID: "dep-c", Content: "digest of the blog", SourceInputID: "digest", SchemaVersion: "v1", Timestamp: at, IngestedAt: at},
			{ID: "dep-d", Content: "second chunk", SourceInputID: "security-blog", SchemaVersion: "v1", Timestamp: at.Add(time.Second), IngestedAt: at.Add(time.Second)},
		} {
			if err := b.conn.Events.Append(ctx, e); err != nil {
				t.Fatalf("%s: append %s: %v", b.name, e.ID, err)
			}
		}

		first := domain.EventDependency{EventID: "dep-b", DependsOn: "dep-a", Kind: domain.DependencyCites, Signal: domain.SignalNamedSource,
			Reference: "Source: the vendor advisory.", CreatedAt: at, CreatedBy: "tester"}
		if err := deps.Append(ctx, []domain.EventDependency{
			first,
			{EventID: "dep-c", DependsOn: "dep-b", Kind: domain.DependencyDerivedFrom, Signal: domain.SignalMetadata, Reference: "derived_from: dep-b", CreatedAt: at, CreatedBy: "tester"},
		}); err != nil {
			t.Fatalf("%s: append dependencies: %v", b.name, err)
		}
		// A repeat of the same pair is ignored: the first justification stands.
		if err := deps.Append(ctx, []domain.EventDependency{{EventID: "dep-b", DependsOn: "dep-a", Kind: domain.DependencyCopies,
			Signal: domain.SignalLink, Reference: "later", CreatedAt: at.Add(time.Hour), CreatedBy: "someone-else"}}); err != nil {
			t.Fatalf("%s: repeat append: %v", b.name, err)
		}
		if err := deps.Append(ctx, []domain.EventDependency{{EventID: "dep-a", DependsOn: "dep-a", Kind: domain.DependencyCites, Signal: domain.SignalLink}}); err == nil {
			t.Errorf("%s: a self-dependency was accepted", b.name)
		}

		out, err := deps.ListByEventIDs(ctx, []string{"dep-b"})
		if err != nil || len(out) != 1 {
			t.Fatalf("%s: outgoing of dep-b = %+v (%v), want one", b.name, out, err)
		}
		if got := out[0]; got.DependsOn != first.DependsOn || got.Kind != first.Kind || got.Signal != first.Signal ||
			got.Reference != first.Reference || got.CreatedBy != first.CreatedBy || !got.CreatedAt.Equal(first.CreatedAt) {
			t.Errorf("%s: stored dependency = %+v, want %+v", b.name, got, first)
		}
		in, err := deps.ListDependents(ctx, []string{"dep-a", "dep-b"})
		if err != nil {
			t.Fatalf("%s: dependents: %v", b.name, err)
		}
		if got := depPairs(in); fmt.Sprint(got) != "[dep-b->dep-a dep-c->dep-b]" {
			t.Errorf("%s: dependents of dep-a, dep-b = %v", b.name, got)
		}

		lister, ok := b.conn.Events.(ports.EventSourceLister)
		if !ok {
			t.Fatalf("%s: events repository is not an EventSourceLister", b.name)
		}
		bySource, err := lister.ListBySourceInputIDs(ctx, []string{"security-blog", "no-such-source"})
		if err != nil {
			t.Fatalf("%s: by source input: %v", b.name, err)
		}
		var ids []string
		for _, e := range bySource {
			ids = append(ids, e.ID)
		}
		if fmt.Sprint(ids) != "[dep-b dep-d]" {
			t.Errorf("%s: events of security-blog = %v, want [dep-b dep-d] oldest first", b.name, ids)
		}

		// Destroying an episode removes its dependencies in both directions.
		if err := b.conn.Events.DeleteByID(ctx, "dep-b"); err != nil {
			t.Fatalf("%s: delete dep-b: %v", b.name, err)
		}
		left, err := deps.ListDependents(ctx, []string{"dep-a", "dep-b"})
		if err != nil || len(left) != 0 {
			t.Errorf("%s: dependencies after deleting dep-b = %v (%v), want none", b.name, depPairs(left), err)
		}
		if err := deps.Append(ctx, []domain.EventDependency{{EventID: "dep-c", DependsOn: "dep-a", Kind: domain.DependencyCites,
			Signal: domain.SignalLink, Reference: "x", CreatedAt: at, CreatedBy: "tester"}}); err != nil {
			t.Fatalf("%s: append: %v", b.name, err)
		}
		if err := b.conn.Claims.DeleteAll(ctx); err != nil {
			t.Fatalf("%s: delete claims: %v", b.name, err)
		}
		if err := b.conn.Events.DeleteAll(ctx); err != nil {
			t.Fatalf("%s: delete all events: %v", b.name, err)
		}
		if all, err := deps.ListByEventIDs(ctx, []string{"dep-c"}); err != nil || len(all) != 0 {
			t.Errorf("%s: dependencies after DeleteAll = %v (%v), want none", b.name, depPairs(all), err)
		}
	}
}

func depPairs(deps []domain.EventDependency) []string {
	out := make([]string, len(deps))
	for i, d := range deps {
		out[i] = d.EventID + "->" + d.DependsOn
	}
	sort.Strings(out)
	return out
}
