package pipeline

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/provenance"
	"go.klarlabs.de/mnemos/internal/store"
)

// MaxLateCitingDocuments bounds how many citing documents one write
// re-detects when it brings a source they cited before it was stored (ADR
// 0038, late citations). Each costs a source lookup plus one resolver pass
// over its references, so the bound keeps a write's cost independent of how
// many documents ever cited what it brings. A source is typically cited by a
// handful of documents; 32 resolves every realistic case in one write. The
// documents past the bound keep their pending rows, oldest citations first are
// resolved, and the rest resolve when another episode carrying the key is
// written.
const MaxLateCitingDocuments = 32

// RecordEventDependencies detects and stores the source dependencies the given
// episodes declare (ADR 0038): citations, links and provenance metadata naming
// a stored source. Every write that stores episodes calls it after the
// episodes are appended (PersistArtifacts, the governed Events write and the
// library's direct episode write), so every ingest path records dependence
// the same way.
//
// It works in both directions. The written episodes' own citations are
// resolved, and the ones that named no stored episode are kept as pending
// references. Then the written episodes are matched against citations left
// pending by earlier writes: a document that cited one of them before it was
// stored is detected again, its dependencies recorded, and the trust of the
// beliefs its episodes evidence rescored.
//
// A backend without a dependency store or a source lister records nothing:
// dependence is then invisible to retraction and trust, exactly as before
// ADR 0038. Every shipped backend has both.
func RecordEventDependencies(ctx context.Context, conn *store.Conn, events []domain.Event) error {
	if conn == nil || conn.Dependencies == nil || len(events) == 0 {
		return nil
	}
	lister, ok := conn.Events.(ports.EventSourceLister)
	if !ok {
		return nil
	}
	lookup := struct {
		ports.EventRepository
		ports.EventSourceLister
	}{conn.Events, lister}
	now := time.Now().UTC()
	found, err := provenance.DetectAll(ctx, lookup, events, now, domain.SystemUser)
	if err != nil {
		return fmt.Errorf("detect episode dependencies: %w", err)
	}
	if len(found.Dependencies) > 0 {
		if err := conn.Dependencies.Append(ctx, found.Dependencies); err != nil {
			return fmt.Errorf("record episode dependencies: %w", err)
		}
	}
	if len(found.Pending) > 0 {
		if err := conn.Dependencies.AppendPending(ctx, found.Pending); err != nil {
			return fmt.Errorf("record pending citations: %w", err)
		}
	}
	if err := resolveLateCitations(ctx, conn, lookup, events, now); err != nil {
		return fmt.Errorf("resolve late citations: %w", err)
	}
	return nil
}

// citingWrite is one earlier write of one citing document: the episodes of a
// source that were ingested together, which is the unit a citation applies to
// at write time (provenance.DetectAll).
type citingWrite struct {
	source   string
	ingested time.Time
	oldest   time.Time // the oldest pending row, for ordering
	citers   []domain.Event
	rows     []domain.PendingReference
}

// resolveLateCitations records the dependencies of documents that cited one of
// the written episodes before it was stored.
func resolveLateCitations(ctx context.Context, conn *store.Conn, lookup provenance.Lookup, events []domain.Event, now time.Time) error {
	written := make(map[string]bool, len(events))
	var keys []string
	seenKey := map[string]bool{}
	for _, ev := range events {
		written[ev.ID] = true
		for _, k := range provenance.EpisodeKeys(ev) {
			if !seenKey[k] {
				seenKey[k] = true
				keys = append(keys, k)
			}
		}
	}
	rows, err := conn.Dependencies.ListPendingByKeys(ctx, keys)
	if err != nil {
		return err
	}
	var citingIDs []string
	byEvent := map[string][]domain.PendingReference{}
	for _, r := range rows {
		if written[r.EventID] {
			continue
		}
		if _, ok := byEvent[r.EventID]; !ok {
			citingIDs = append(citingIDs, r.EventID)
		}
		byEvent[r.EventID] = append(byEvent[r.EventID], r)
	}
	if len(citingIDs) == 0 {
		return nil
	}
	citers, err := conn.Events.ListByIDs(ctx, citingIDs)
	if err != nil {
		return fmt.Errorf("load citing episodes: %w", err)
	}
	writes := groupCitingWrites(citers, byEvent)
	if len(writes) > MaxLateCitingDocuments {
		writes = writes[:MaxLateCitingDocuments]
	}

	var (
		deps     []domain.EventDependency
		resolved []domain.PendingReference
		bySource = map[string][]domain.Event{}
	)
	for _, w := range writes {
		stored, ok := bySource[w.source]
		if !ok {
			if stored, err = lookup.ListBySourceInputIDs(ctx, []string{w.source}); err != nil {
				return fmt.Errorf("load citing document %q: %w", w.source, err)
			}
			bySource[w.source] = stored
		}
		// The document's episodes from the citing write. A citation applies to
		// every episode of its document in the write that stored it, and to no
		// episode a different write added under the same source id (a capture
		// stream shares one).
		var doc []domain.Event
		inDoc := map[string]bool{}
		for _, e := range stored {
			if e.IngestedAt.Equal(w.ingested) {
				doc = append(doc, e)
				inDoc[e.ID] = true
			}
		}
		for _, e := range w.citers {
			if !inDoc[e.ID] {
				doc = append(doc, e)
			}
		}
		found, err := provenance.DetectAll(ctx, lookup, doc, now, domain.SystemUser)
		if err != nil {
			return fmt.Errorf("detect late dependencies of %q: %w", w.source, err)
		}
		deps = append(deps, found.Dependencies...)
		// A matched row whose key the document still cannot resolve (a key
		// shared by a spelling the resolver does not accept) stays pending.
		still := map[[2]string]bool{}
		for _, p := range found.Pending {
			still[[2]string{p.EventID, p.LookupKey}] = true
		}
		for _, r := range w.rows {
			if !still[[2]string{r.EventID, r.LookupKey}] {
				resolved = append(resolved, r)
			}
		}
	}
	if len(deps) > 0 {
		if err := conn.Dependencies.Append(ctx, deps); err != nil {
			return fmt.Errorf("record late dependencies: %w", err)
		}
	}
	if len(resolved) > 0 {
		if err := conn.Dependencies.DeletePending(ctx, resolved); err != nil {
			return fmt.Errorf("delete resolved citations: %w", err)
		}
	}
	return rescoreDependentBeliefs(ctx, conn, deps)
}

// groupCitingWrites groups the citing episodes into the writes of their
// documents, oldest pending citation first, so the bound resolves the
// longest-waiting citations first and the order is the same on every run.
func groupCitingWrites(citers []domain.Event, byEvent map[string][]domain.PendingReference) []*citingWrite {
	type key struct {
		source   string
		ingested int64
	}
	var out []*citingWrite
	index := map[key]*citingWrite{}
	for _, e := range citers {
		k := key{e.SourceInputID, e.IngestedAt.UnixNano()}
		w := index[k]
		if w == nil {
			w = &citingWrite{source: e.SourceInputID, ingested: e.IngestedAt}
			index[k] = w
			out = append(out, w)
		}
		w.citers = append(w.citers, e)
		for _, r := range byEvent[e.ID] {
			if w.oldest.IsZero() || r.CreatedAt.Before(w.oldest) {
				w.oldest = r.CreatedAt
			}
			w.rows = append(w.rows, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.oldest.Equal(b.oldest) {
			return a.oldest.Before(b.oldest)
		}
		if a.source != b.source {
			return a.source < b.source
		}
		return a.ingested.Before(b.ingested)
	})
	return out
}

// rescoreDependentBeliefs rescores the beliefs evidenced by the episodes a
// late dependency made dependent. Trust counts an evidence episode that
// depends on another evidence episode of the same belief as a repeat, not a
// distinct source (ADR 0038 §4), so only those beliefs can change: the
// dependency's citing side bounds the set, and the scored inputs decide.
func rescoreDependentBeliefs(ctx context.Context, conn *store.Conn, deps []domain.EventDependency) error {
	if len(deps) == 0 {
		return nil
	}
	var citing []string
	seen := map[string]bool{}
	for _, d := range deps {
		if !seen[d.EventID] {
			seen[d.EventID] = true
			citing = append(citing, d.EventID)
		}
	}
	claims, err := conn.Claims.ListByEventIDs(ctx, citing)
	if err != nil {
		return fmt.Errorf("list beliefs of late citing episodes: %w", err)
	}
	if len(claims) == 0 {
		return nil
	}
	ids := make([]string, 0, len(claims))
	for _, c := range claims {
		ids = append(ids, c.ID)
	}
	if scoped, ok := conn.Claims.(ports.ScopedTrustScorer); ok {
		if _, err := scoped.RecomputeTrustForClaims(ctx, ids, defaultTrustScorer()); err != nil {
			return fmt.Errorf("rescore beliefs of late citing episodes: %w", err)
		}
		return nil
	}
	if scorer, ok := conn.Claims.(ports.TrustScorer); ok {
		if _, err := scorer.RecomputeTrust(ctx, defaultTrustScorer()); err != nil {
			return fmt.Errorf("rescore beliefs of late citing episodes: %w", err)
		}
	}
	return nil
}
