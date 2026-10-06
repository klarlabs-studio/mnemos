package mnemos

import (
	"context"

	"go.klarlabs.de/mnemos/internal/domain"
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
}

func (m *memory) newHealthCorpus() *healthCorpus { return &healthCorpus{m: m} }

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
