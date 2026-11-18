// Package kbmetrics computes the knowledge-base counts every metrics surface
// reports: REST GET /v1/metrics, the gRPC Metrics RPC, the knowledge_metrics MCP
// tool and the library's Memory.Metrics. Each surface used to count on its own,
// and they disagreed about what a dissonance was and whether a run-scoped token
// saw the whole brain. One function now owns the arithmetic; surfaces only
// translate the result to their wire form.
package kbmetrics

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// Counts is the knowledge-base summary, in machine terms.
type Counts struct {
	Runs            int64
	Events          int64
	Claims          int64
	ContestedClaims int64
	Relationships   int64
	Contradictions  int64
	Embeddings      int64
}

// Compute counts the knowledge in conn. A non-empty allowedRuns narrows every
// count to the episodes of those runs and the claims they evidence, which is
// the F.4.b run-scope boundary: a run-scoped token never learns the size of the
// rest of the brain. Embeddings count when their entity is visible.
func Compute(ctx context.Context, conn *store.Conn, allowedRuns []string) (Counts, error) {
	events, err := conn.Events.ListAll(ctx)
	if err != nil {
		return Counts{}, fmt.Errorf("list episodes: %w", err)
	}
	claims, err := conn.Claims.ListAll(ctx)
	if err != nil {
		return Counts{}, fmt.Errorf("list beliefs: %w", err)
	}
	eventEmbs, err := conn.Embeddings.ListByEntityType(ctx, "event")
	if err != nil {
		return Counts{}, fmt.Errorf("list episode embeddings: %w", err)
	}
	claimEmbs, err := conn.Embeddings.ListByEntityType(ctx, "claim")
	if err != nil {
		return Counts{}, fmt.Errorf("list belief embeddings: %w", err)
	}

	if len(allowedRuns) > 0 {
		allowed := make(map[string]struct{}, len(allowedRuns))
		for _, r := range allowedRuns {
			allowed[r] = struct{}{}
		}
		keptEvents := events[:0]
		visibleEvents := map[string]struct{}{}
		for _, e := range events {
			if _, ok := allowed[e.RunID]; ok {
				keptEvents = append(keptEvents, e)
				visibleEvents[e.ID] = struct{}{}
			}
		}
		events = keptEvents

		// A claim's run is only reachable through its evidence links.
		visibleClaims := map[string]struct{}{}
		if len(claims) > 0 && len(visibleEvents) > 0 {
			links, err := conn.Claims.ListEvidenceByClaimIDs(ctx, claimIDs(claims))
			if err != nil {
				return Counts{}, fmt.Errorf("load evidence for run scope: %w", err)
			}
			for _, l := range links {
				if _, ok := visibleEvents[l.EventID]; ok {
					visibleClaims[l.ClaimID] = struct{}{}
				}
			}
		}
		keptClaims := claims[:0]
		for _, c := range claims {
			if _, ok := visibleClaims[c.ID]; ok {
				keptClaims = append(keptClaims, c)
			}
		}
		claims = keptClaims
		eventEmbs = keepEmbeddings(eventEmbs, visibleEvents)
		claimEmbs = keepEmbeddings(claimEmbs, visibleClaims)
	}

	out := Counts{
		Events:     int64(len(events)),
		Claims:     int64(len(claims)),
		Embeddings: int64(len(eventEmbs) + len(claimEmbs)),
	}
	runs := map[string]struct{}{}
	for _, e := range events {
		if e.RunID != "" {
			runs[e.RunID] = struct{}{}
		}
	}
	out.Runs = int64(len(runs))
	for _, c := range claims {
		if c.Status == domain.ClaimStatusContested {
			out.ContestedClaims++
		}
	}
	// Associations and dissonances are counted from the surviving claims, so
	// the run narrowing above carries into them without a second filter.
	rels, err := conn.Relationships.ListByClaimIDs(ctx, claimIDs(claims))
	if err != nil {
		return Counts{}, fmt.Errorf("list associations: %w", err)
	}
	out.Relationships = int64(len(rels))
	for _, rel := range rels {
		if rel.Type == domain.RelationshipTypeContradicts {
			out.Contradictions++
		}
	}
	return out, nil
}

func claimIDs(claims []domain.Claim) []string {
	ids := make([]string, 0, len(claims))
	for _, c := range claims {
		ids = append(ids, c.ID)
	}
	return ids
}

func keepEmbeddings(in []domain.EmbeddingRecord, visible map[string]struct{}) []domain.EmbeddingRecord {
	out := in[:0]
	for _, e := range in {
		if _, ok := visible[e.EntityID]; ok {
			out = append(out, e)
		}
	}
	return out
}
