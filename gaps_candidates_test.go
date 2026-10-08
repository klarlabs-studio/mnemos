package mnemos

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// The store's gap candidates produce the same report as the full sweep: the
// same gaps, scores and order, and the same bounds (Scanned and Considered
// count every open claim, though the candidate path reads only hypotheses and
// contested claims). The brain carries every case the sweep distinguishes:
// open and closed hypotheses, resolved and unresolved ones, claims with one,
// two and three contradictions, deprecated claims (still in the sweep's
// population), and contradictions with a missing endpoint.
func TestKnowledgeGaps_CandidatesEqualTheFullSweep(t *testing.T) {
	for name, dsn := range healthBackends(t) {
		t.Run(name, func(t *testing.T) {
			m, _ := healthBrain(t, dsn)
			ctx := context.Background()
			now := time.Now().UTC()
			var hyps []domain.Claim
			for i := 0; i < 12; i++ {
				c := domain.Claim{ID: fmt.Sprintf("hyp-%02d", i), Text: fmt.Sprintf("maybe cause %d", i), Type: domain.ClaimTypeHypothesis,
					Confidence: 0.3 + 0.05*float64(i), Status: domain.ClaimStatusActive, CreatedAt: now.Add(-time.Duration(i) * 24 * time.Hour)}
				if i%4 == 3 {
					c.ValidTo = now.Add(-time.Hour) // closed: never a gap
				}
				hyps = append(hyps, c)
			}
			if err := m.conn.Claims.Upsert(ctx, hyps); err != nil {
				t.Fatal(err)
			}
			for _, c := range hyps {
				if !c.ValidTo.IsZero() {
					if err := m.conn.Claims.SetValidity(ctx, c.ID, c.ValidTo); err != nil {
						t.Fatal(err)
					}
				}
			}
			adjudicate(t, m, "v1", "hyp-01", true, now)
			adjudicate(t, m, "v2", "hyp-02", false, now)
			// h-050 gets three contradictions, h-060 two, h-070 one.
			var rels []domain.Relationship
			for i, pair := range [][2]string{{"h-050", "h-051"}, {"h-052", "h-050"}, {"h-050", "h-053"}, {"h-060", "h-061"}, {"h-062", "h-060"}, {"h-070", "h-071"}, {"h-055", "h-044"}} {
				rels = append(rels, domain.Relationship{ID: fmt.Sprintf("g-%d", i), Type: domain.RelationshipTypeContradicts,
					FromClaimID: pair[0], ToClaimID: pair[1], CreatedAt: now})
			}
			if err := m.conn.Relationships.Upsert(ctx, rels); err != nil {
				t.Fatal(err)
			}

			for _, limit := range []int{3, 500} {
				gapsFullSweep = true
				full, err := m.KnowledgeGapsBounded(ctx, limit)
				gapsFullSweep = false
				if err != nil {
					t.Fatal(err)
				}
				cand, err := m.KnowledgeGapsBounded(ctx, limit)
				if err != nil {
					t.Fatal(err)
				}
				// Staleness reads time.Now, which moves between the two calls:
				// scores agree to well within 1e-6, everything else exactly.
				if len(full.Gaps) == len(cand.Gaps) {
					for i := range full.Gaps {
						if math.Abs(full.Gaps[i].Score-cand.Gaps[i].Score) < 1e-6 {
							cand.Gaps[i].Score = full.Gaps[i].Score
						}
					}
				}
				if !reflect.DeepEqual(full, cand) {
					t.Fatalf("limit %d: candidates differ from the full sweep:\nfull: %+v\ncand: %+v", limit, full, cand)
				}
				if limit == 500 {
					kinds := map[string]int{}
					for _, g := range full.Gaps {
						kinds[g.Kind]++
					}
					if kinds[GapUnresolvedHypothesis] == 0 || kinds[GapContested] == 0 {
						t.Fatalf("gaps %v do not exercise both kinds", kinds)
					}
				}
			}
		})
	}
}
