package pipeline

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/relate"
)

// CapStoredContradictions drops inferred contradiction edges that would take
// either endpoint past relate.MaxContradictionsPerClaim stored contradictions.
// It returns the edges to write and how many it dropped.
//
// A detection pass caps what it emits, but the incremental path only sees one
// write at a time: an existing claim can pick up an edge from every write that
// mentions its subject. On a real brain that is how 1,102 claims came to hold
// more than 100 contradictions each. This guard counts what is already stored,
// so the cap holds across writes.
//
// Only edges the rule-based detectors inferred (DerivedBy "relate/…") are
// capped. A contradiction a caller asserts explicitly is written as given, and
// re-writing a pair that is already stored never counts against the cap.
//
// If the stored edges cannot be read, the edges are written uncapped and the
// failure is logged: losing the guard on one write is better than losing the
// write.
func CapStoredContradictions(ctx context.Context, repo ports.RelationshipRepository, rels []domain.Relationship) ([]domain.Relationship, int) {
	limit := relate.MaxContradictionsPerClaim
	var capped []int
	endpoints := map[string]struct{}{}
	for i, r := range rels {
		if r.Type != domain.RelationshipTypeContradicts || !strings.HasPrefix(r.DerivedBy, "relate/") {
			continue
		}
		capped = append(capped, i)
		endpoints[r.FromClaimID] = struct{}{}
		endpoints[r.ToClaimID] = struct{}{}
	}
	if len(capped) == 0 || repo == nil {
		return rels, 0
	}

	ids := make([]string, 0, len(endpoints))
	for id := range endpoints {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	stored, err := repo.ListByClaimIDs(ctx, ids)
	if err != nil {
		slog.WarnContext(ctx, "contradiction cap skipped: stored edges unreadable", "claims", len(ids), "error", err)
		return rels, 0
	}

	partners := map[string]map[string]struct{}{}
	link := func(a, b string) {
		if partners[a] == nil {
			partners[a] = map[string]struct{}{}
		}
		partners[a][b] = struct{}{}
	}
	for _, r := range stored {
		if r.Type == domain.RelationshipTypeContradicts {
			link(r.FromClaimID, r.ToClaimID)
			link(r.ToClaimID, r.FromClaimID)
		}
	}

	// Strongest first, so a full claim keeps its best new edges.
	slices.SortStableFunc(capped, func(x, y int) int {
		return cmp.Compare(rels[y].EffectiveStrength(), rels[x].EffectiveStrength())
	})
	drop := make(map[int]bool)
	for _, i := range capped {
		r := rels[i]
		if _, known := partners[r.FromClaimID][r.ToClaimID]; known {
			continue
		}
		if len(partners[r.FromClaimID]) >= limit || len(partners[r.ToClaimID]) >= limit {
			drop[i] = true
			continue
		}
		link(r.FromClaimID, r.ToClaimID)
		link(r.ToClaimID, r.FromClaimID)
	}
	if len(drop) == 0 {
		return rels, 0
	}
	out := make([]domain.Relationship, 0, len(rels)-len(drop))
	for i, r := range rels {
		if !drop[i] {
			out = append(out, r)
		}
	}
	return out, len(drop)
}
