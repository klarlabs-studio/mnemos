package mnemos

import (
	"context"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// healthCorpus memoises the whole-brain reads of one health computation.
//
// BrainHealth reached the claims table through four helpers — itself,
// Calibration (twice: directly and inside PredictiveError),
// hypercorrectionList and liveBeliefCount — and loaded it once per helper:
// five full ListAll calls per BrainHealth, each sorting the table. At 1M
// beliefs that was most of a 57 s health check. One computation now reads each
// table at most once and every helper sees the same snapshot, which is also
// more correct: the vitals used to be computed over reads that could straddle
// a concurrent write.
//
// Loads stay lazy: a helper that never needs claims (no contradictions, no
// adjudicated beliefs) still never reads them.
type healthCorpus struct {
	m *memory

	claims       []domain.Claim
	claimsLoaded bool

	rels       []domain.Relationship
	relsLoaded bool

	cal       Calibration
	calLoaded bool

	// liveCount, when set (>= 0), is the exact live-belief count a store
	// computed in SQL, so the dissonance denominator needs no full load.
	liveCount int

	// sampler, when set, counts hypercorrections in the store instead of
	// loading both endpoints of every contradiction (sampled health only).
	sampler ports.HealthSampler
}

func (m *memory) newHealthCorpus() *healthCorpus { return &healthCorpus{m: m, liveCount: -1} }

// Claims returns every stored claim, read on first use.
func (h *healthCorpus) Claims(ctx context.Context) ([]domain.Claim, error) {
	if !h.claimsLoaded {
		c, err := h.m.conn.Claims.ListAll(ctx)
		if err != nil {
			return nil, err
		}
		h.claims, h.claimsLoaded = c, true
	}
	return h.claims, nil
}

// Relationships returns every stored relationship, read on first use.
func (h *healthCorpus) Relationships(ctx context.Context) ([]domain.Relationship, error) {
	if !h.relsLoaded {
		r, err := h.m.conn.Relationships.ListAll(ctx)
		if err != nil {
			return nil, err
		}
		h.rels, h.relsLoaded = r, true
	}
	return h.rels, nil
}

// Calibration is memory.Calibration over this snapshot, computed once.
func (h *healthCorpus) Calibration(ctx context.Context) (Calibration, error) {
	if !h.calLoaded {
		c, err := h.m.calibrationIn(ctx, h)
		if err != nil {
			return Calibration{}, err
		}
		h.cal, h.calLoaded = c, true
	}
	return h.cal, nil
}

// ClaimsByID returns the stored claims among ids. It reads only those claims
// unless the full table is already loaded, so a helper that needs a handful of
// beliefs (contradiction endpoints, adjudicated beliefs) no longer forces a
// whole-brain read.
func (h *healthCorpus) ClaimsByID(ctx context.Context, ids []string) (map[string]domain.Claim, error) {
	out := make(map[string]domain.Claim, len(ids))
	if h.claimsLoaded {
		want := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			want[id] = struct{}{}
		}
		for _, c := range h.claims {
			if _, ok := want[c.ID]; ok {
				out[c.ID] = c
			}
		}
		return out, nil
	}
	for start := 0; start < len(ids); start += 5000 {
		cs, err := h.m.conn.Claims.ListByIDs(ctx, ids[start:min(start+5000, len(ids))])
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			out[c.ID] = c
		}
	}
	return out, nil
}

// Contradictions returns the contradicts edges in ListAll's order: filtered
// from the loaded edges when the exact path already read them all, otherwise
// listed by type when the store can, otherwise from a full read.
func (h *healthCorpus) Contradictions(ctx context.Context) ([]domain.Relationship, error) {
	if !h.relsLoaded {
		if l, ok := h.m.conn.Relationships.(ports.RelationshipTypeLister); ok {
			return l.ListByType(ctx, domain.RelationshipTypeContradicts)
		}
	}
	rels, err := h.Relationships(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.Relationship
	for _, r := range rels {
		if r.Type == domain.RelationshipTypeContradicts {
			out = append(out, r)
		}
	}
	return out, nil
}
