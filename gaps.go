package mnemos

import (
	"context"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
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

// GapDefaultLimit is the gap count [Memory.KnowledgeGaps] returns when the
// caller names none.
const GapDefaultLimit = 20

// KnowledgeGaps implements [Memory.KnowledgeGaps]. The result count is bounded
// by [MaxCognitiveResults]; use [memory.KnowledgeGapsBounded] to learn whether
// the answer was truncated.
func (m *memory) KnowledgeGaps(ctx context.Context, limit int) ([]Gap, error) {
	rep, err := m.KnowledgeGapsBounded(ctx, limit)
	return rep.Gaps, err
}

// KnowledgeGapsBounded is [Memory.KnowledgeGaps] reporting the bounds it applied
// (see [BoundedCognition]).
//
// The detection pass is a single linear sweep of the live corpus — measured at
// 4 ms per 10k claims, so it is NOT the resource-exhaustion vector the pairwise
// reads were, and it is deliberately left complete: scoring a ranked prefix of
// the corpus would return arbitrary gaps rather than the biggest ones. What is
// bounded is the RESPONSE, and Bounds.Available reports the true gap count.
func (m *memory) KnowledgeGapsBounded(ctx context.Context, limit int) (GapReport, error) {
	var bounds Bounds
	limit = capLimit(&bounds, limit, GapDefaultLimit, MaxCognitiveResults)
	in, err := m.gapInputs(ctx)
	if err != nil {
		return GapReport{}, err
	}
	all, evidenceCount, resolved, contradicts := in.claims, in.evidence, in.resolved, in.contradicts
	now := time.Now().UTC()
	var gaps []Gap
	for _, c := range all {
		if !c.ValidTo.IsZero() {
			continue // only live knowledge
		}
		bounds.Scanned++
		bounds.Considered++
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
	// Open claims the inputs left out (neither hypotheses nor contested, so
	// never gaps) are still part of the sweep's population: Scanned and
	// Considered count every open claim, as the full read always did.
	skipped := in.openClaims - bounds.Scanned
	bounds.Scanned += skipped
	bounds.Considered += skipped
	bounds.Available = len(gaps)
	// Rank by expected information gain, THEN cut; id breaks ties so equal-score
	// queues are stable.
	gaps = topN(gaps, limit, func(a, b Gap) bool {
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.ClaimID < b.ClaimID
	})
	if bounds.Available > len(gaps) {
		bounds.cut(BoundReasonResultLimit)
	}
	bounds.finish(len(gaps))
	return GapReport{Gaps: gaps, Bounds: bounds}, nil
}

// gapsFullSweep forces the full read even where the store offers candidates,
// so tests can compare the two.
var gapsFullSweep = false

// gapInputs is what the gap sweep scores: the claims that can be gaps, their
// evidence and contradiction counts, which hypotheses carry a verdict, and how
// many open-validity claims the sweep covers in all.
type gapInputs struct {
	claims      []domain.Claim
	evidence    map[string]int
	contradicts map[string]int
	resolved    map[string]struct{}
	openClaims  int
}

// gapInputs gathers them from the store's candidates when it can answer
// ports.GapCandidateSource, and from a full read otherwise. Both yield the same
// report (TestKnowledgeGaps_CandidatesEqualTheFullSweep): a claim that is
// neither a hypothesis nor contested is never a gap, so leaving it unread
// changes nothing but the cost.
func (m *memory) gapInputs(ctx context.Context) (gapInputs, error) {
	resolved, err := m.gapVerdicts(ctx)
	if err != nil {
		return gapInputs{}, err
	}
	if src, ok := m.conn.Claims.(ports.GapCandidateSource); ok && !gapsFullSweep {
		c, err := src.GapCandidates(ctx, GapContestedThreshold)
		if err != nil {
			return gapInputs{}, fmt.Errorf("mnemos: KnowledgeGaps: candidates: %w", err)
		}
		return gapInputs{claims: c.Claims, evidence: c.Evidence, contradicts: c.Contradictions, resolved: resolved, openClaims: c.OpenClaims}, nil
	}
	return m.gapInputsFull(ctx, resolved)
}

// gapVerdicts is which claims already carry a validates/refutes verdict (from
// outcome edges).
func (m *memory) gapVerdicts(ctx context.Context) (map[string]struct{}, error) {
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
	return resolved, nil
}

// gapInputsFull reads every claim, evidence link and relationship.
func (m *memory) gapInputsFull(ctx context.Context, resolved map[string]struct{}) (gapInputs, error) {
	all, err := m.conn.Claims.ListAll(ctx)
	if err != nil {
		return gapInputs{}, fmt.Errorf("mnemos: KnowledgeGaps: list claims: %w", err)
	}
	evidence, err := m.conn.Claims.ListAllEvidence(ctx)
	if err != nil {
		return gapInputs{}, fmt.Errorf("mnemos: KnowledgeGaps: list evidence: %w", err)
	}
	evidenceCount := make(map[string]int, len(all))
	for _, e := range evidence {
		evidenceCount[e.ClaimID]++
	}
	// Contradiction density per claim (claim↔claim graph).
	contradicts := map[string]int{}
	rels, err := m.conn.Relationships.ListAll(ctx)
	if err != nil {
		return gapInputs{}, fmt.Errorf("mnemos: KnowledgeGaps: list relationships: %w", err)
	}
	for _, r := range rels {
		if r.Type != domain.RelationshipTypeContradicts {
			continue
		}
		contradicts[r.FromClaimID]++
		contradicts[r.ToClaimID]++
	}

	open := 0
	for _, c := range all {
		if c.ValidTo.IsZero() {
			open++
		}
	}
	return gapInputs{claims: all, evidence: evidenceCount, contradicts: contradicts, resolved: resolved, openClaims: open}, nil
}
