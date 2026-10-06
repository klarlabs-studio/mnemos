// Package browse pages the REST browse APIs (#382 Phase 5): keyset cursors
// over the store when it can page in SQL, and an in-process definition the
// store pagers are tested against.
package browse

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/page"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"
)

// Beliefs returns one page of the belief browse. With no offset it pages
// in the store when the store can (ports.ClaimPager): filter, order and cut in
// SQL, reading only the page. Otherwise, and always for the legacy offset
// form, it pages in Go over the full list. Both orders are newest first with
// id breaking ties, and both honour the cursor.
func Beliefs(ctx context.Context, conn *store.Conn, f page.ClaimFilter, after *page.Key, limit, offset int) (ports.ClaimPage, error) {
	if offset == 0 {
		if p, ok := conn.Claims.(ports.ClaimPager); ok {
			pg, err := p.PageClaims(ctx, f, after, limit)
			if !errors.Is(err, ports.ErrPageUnsupported) {
				return pg, err
			}
		}
	}
	return BeliefsInGo(ctx, conn, f, after, limit, offset)
}

// BeliefsInGo filters the whole belief list in process. It is the
// fallback for stores without a pager and for filters a store cannot evaluate
// exactly, and the definition the store pagers are tested against.
func BeliefsInGo(ctx context.Context, conn *store.Conn, f page.ClaimFilter, after *page.Key, limit, offset int) (ports.ClaimPage, error) {
	// run_id scoping: the beliefs with evidence from an event of that run. An
	// empty set means the run has no beliefs; return nothing rather than fall
	// through to an unfiltered list.
	var allowedEventIDs map[string]struct{}
	if f.RunID != "" {
		events, err := conn.Events.ListByRunID(ctx, f.RunID)
		if err != nil {
			return ports.ClaimPage{}, fmt.Errorf("list events by run id: %w", err)
		}
		if len(events) == 0 {
			return ports.ClaimPage{}, nil
		}
		allowedEventIDs = make(map[string]struct{}, len(events))
		for _, e := range events {
			allowedEventIDs[e.ID] = struct{}{}
		}
	}
	all, err := conn.Claims.ListAll(ctx)
	if err != nil {
		return ports.ClaimPage{}, err
	}
	filtered := all[:0]
	for _, c := range all {
		if f.Matches(c) {
			filtered = append(filtered, c)
		}
	}
	// The run filter goes last, so the evidence load covers only beliefs the
	// cheaper filters kept.
	if allowedEventIDs != nil && len(filtered) > 0 {
		ids := make([]string, 0, len(filtered))
		for _, c := range filtered {
			ids = append(ids, c.ID)
		}
		links, err := conn.Claims.ListEvidenceByClaimIDs(ctx, ids)
		if err != nil {
			return ports.ClaimPage{}, fmt.Errorf("list evidence for run_id filter: %w", err)
		}
		inRun := make(map[string]bool, len(links))
		for _, l := range links {
			if _, ok := allowedEventIDs[l.EventID]; ok {
				inRun[l.ClaimID] = true
			}
		}
		kept := filtered[:0]
		for _, c := range filtered {
			if inRun[c.ID] {
				kept = append(kept, c)
			}
		}
		filtered = kept
	}
	sort.Slice(filtered, func(i, j int) bool {
		return page.Newer(filtered[i].CreatedAt, filtered[i].ID, filtered[j].CreatedAt, filtered[j].ID)
	})
	out := ports.ClaimPage{Total: len(filtered)}
	start := offset
	if after != nil {
		start = sort.Search(len(filtered), func(i int) bool {
			return after.After(filtered[i].CreatedAt, filtered[i].ID)
		})
	}
	rest := []domain.Claim{}
	if start < len(filtered) {
		rest = filtered[start:]
	}
	if len(rest) > limit {
		out.More = true
		rest = rest[:limit]
	}
	out.Claims = rest
	return out, nil
}
