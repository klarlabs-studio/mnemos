package mnemos

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/trust"
)

// Gap kinds surfaced by [Memory.KnowledgeGaps].
const (
	// GapUnresolvedHypothesis is a hypothesis with no validates/refutes verdict —
	// something predicted but never confirmed or ruled out.
	GapUnresolvedHypothesis = "unresolved_hypothesis"
	// GapContested is a claim with multiple live contradictions — knowledge that
	// needs reconciling.
	GapContested = "contested"
)

// GapContestedThreshold is the minimum number of contradiction edges for a claim
// to count as contested.
const GapContestedThreshold = 2

// Gap is one weak spot in the store — a place worth investigating next, ranked by
// expected information gain.
type Gap struct {
	ClaimID string
	Text    string
	Kind    string  // GapUnresolvedHypothesis | GapContested
	Score   float64 // expected information gain: salience × uncertainty × staleness
}

// KnowledgeGaps implements [Memory.KnowledgeGaps].
func (m *memory) KnowledgeGaps(ctx context.Context, limit int) ([]Gap, error) {
	all, err := m.conn.Claims.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("mnemos: KnowledgeGaps: list claims: %w", err)
	}
	evidence, err := m.conn.Claims.ListAllEvidence(ctx)
	if err != nil {
		return nil, fmt.Errorf("mnemos: KnowledgeGaps: list evidence: %w", err)
	}
	evidenceCount := make(map[string]int, len(all))
	for _, e := range evidence {
		evidenceCount[e.ClaimID]++
	}
	// Which claims already carry a validates/refutes verdict (from outcome edges).
	resolved := map[string]struct{}{}
	for _, kind := range []domain.RelationshipType{domain.RelationshipTypeValidates, domain.RelationshipTypeRefutes} {
		edges, eerr := m.conn.EntityRels.ListByKind(ctx, string(kind))
		if eerr != nil {
			return nil, fmt.Errorf("mnemos: KnowledgeGaps: list %s: %w", kind, eerr)
		}
		for _, e := range edges {
			if e.ToType == domain.RelEntityClaim && e.ToID != "" {
				resolved[e.ToID] = struct{}{}
			}
		}
	}
	// Contradiction density per claim (claim↔claim graph).
	contradicts := map[string]int{}
	rels, err := m.conn.Relationships.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("mnemos: KnowledgeGaps: list relationships: %w", err)
	}
	for _, r := range rels {
		if r.Type != domain.RelationshipTypeContradicts {
			continue
		}
		contradicts[r.FromClaimID]++
		contradicts[r.ToClaimID]++
	}

	now := time.Now().UTC()
	var gaps []Gap
	for _, c := range all {
		if !c.ValidTo.IsZero() {
			continue // only live knowledge
		}
		kind := ""
		if c.Type == domain.ClaimTypeHypothesis {
			if _, ok := resolved[c.ID]; !ok {
				kind = GapUnresolvedHypothesis
			}
		}
		if kind == "" && contradicts[c.ID] >= GapContestedThreshold {
			kind = GapContested
		}
		if kind == "" {
			continue
		}
		uncertainty := 1 - clamp01(c.TrustScore)
		staleness := 1 - replayRecency(c, now) // 0 fresh → 1 old
		// Staleness modulates but never zeroes a fresh gap (a fresh unresolved
		// hypothesis is still worth chasing).
		score := trust.SalienceOf(c, evidenceCount[c.ID]) * uncertainty * (0.3 + 0.7*staleness)
		gaps = append(gaps, Gap{ClaimID: c.ID, Text: c.Text, Kind: kind, Score: score})
	}
	sort.SliceStable(gaps, func(i, j int) bool { return gaps[i].Score > gaps[j].Score })
	if limit > 0 && len(gaps) > limit {
		gaps = gaps[:limit]
	}
	return gaps, nil
}
