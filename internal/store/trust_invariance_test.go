package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/trust"
)

// THE trust invariant (ADR 0026): for a given belief, evidence set, model
// version and instant, every subsystem derives the same trust.
//
// Subsystems reach trust two ways. Get, the API, recall and the context block
// read the STORED score; brain health and float-back evaluate trust.At on
// ListTrustInputs at their own instant. So the invariant holds exactly when the
// stored score is trust.At of the lister's inputs at the stamped instant — on
// every backend, for every shape of belief. That is what this asserts, over a
// corpus built to exercise each input: a per-belief time constant, explicit
// confirmation, applied credit, independent and repeated sources, and none.
func TestTrust_StoredEqualsCanonicalForEveryBeliefAcrossBackends(t *testing.T) {
	backends := openBackends(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	type shape struct {
		id         string
		halfLife   float64
		confirmed  time.Time
		credit     float64
		evidence   []string // event ids
		confidence float64
	}
	shapes := []shape{
		{id: "plain", confidence: 0.8, evidence: []string{"e-old"}},
		{id: "volatile", confidence: 0.9, halfLife: 14, evidence: []string{"e-old"}},
		{id: "confirmed", confidence: 0.9, confirmed: at.Add(-2 * day), evidence: []string{"e-ancient"}},
		{id: "credited", confidence: 0.6, credit: -0.2, evidence: []string{"e-new"}},
		{id: "multi-source", confidence: 0.7, evidence: []string{"e-new", "e-old", "e-bob"}},
		{id: "no-evidence", confidence: 0.5},
	}
	events := map[string]struct {
		ts  time.Time
		who string
	}{
		"e-new":     {at.Add(-1 * day), "alice"},
		"e-old":     {at.Add(-40 * day), "alice"},
		"e-ancient": {at.Add(-400 * day), "alice"},
		"e-bob":     {at.Add(-10 * day), "bob"},
	}

	for _, b := range backends {
		for id, e := range events {
			if err := b.conn.Events.Append(ctx, domain.Event{
				ID: id, Content: "source " + id, SchemaVersion: "v1", SourceInputID: "src-" + id,
				Timestamp: e.ts, IngestedAt: e.ts, CreatedBy: e.who,
			}); err != nil {
				t.Fatalf("%s: append %s: %v", b.name, id, err)
			}
		}
		for _, s := range shapes {
			if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{
				ID: s.id, Text: "belief " + s.id, Type: domain.ClaimTypeFact,
				Confidence: s.confidence, Status: domain.ClaimStatusActive, CreatedAt: at.Add(-500 * day),
			}}); err != nil {
				t.Fatalf("%s: upsert %s: %v", b.name, s.id, err)
			}
			var links []domain.ClaimEvidence
			for _, ev := range s.evidence {
				links = append(links, domain.ClaimEvidence{ClaimID: s.id, EventID: ev})
			}
			if len(links) > 0 {
				if err := b.conn.Claims.UpsertEvidence(ctx, links); err != nil {
					t.Fatalf("%s: link %s: %v", b.name, s.id, err)
				}
			}
			if s.halfLife > 0 {
				if err := b.conn.Claims.MarkVerified(ctx, s.id, at.Add(-500*day), s.halfLife); err != nil {
					t.Fatalf("%s: half-life %s: %v", b.name, s.id, err)
				}
			}
			if !s.confirmed.IsZero() {
				if err := b.conn.Claims.MarkConfirmed(ctx, s.id, s.confirmed); err != nil {
					t.Fatalf("%s: confirm %s: %v", b.name, s.id, err)
				}
			}
			if s.credit != 0 {
				if err := b.conn.Claims.(ports.BeliefCreditWriter).ApplyBeliefCredit(ctx, s.id,
					map[string]float64{domain.CreditAppliedComponentKey: s.credit}, 0); err != nil {
					t.Fatalf("%s: credit %s: %v", b.name, s.id, err)
				}
			}
		}

		if _, err := b.conn.Claims.(ports.TrustScorer).RecomputeTrust(ctx, trust.Scorer(at)); err != nil {
			t.Fatalf("%s: recompute: %v", b.name, err)
		}
		inputs, err := b.conn.Claims.(ports.TrustInputLister).ListTrustInputs(ctx, nil)
		if err != nil {
			t.Fatalf("%s: list trust inputs: %v", b.name, err)
		}
		ids := make([]string, len(shapes))
		for i, s := range shapes {
			ids[i] = s.id
		}
		stored, err := b.conn.Claims.ListByIDs(ctx, ids)
		if err != nil {
			t.Fatalf("%s: read back: %v", b.name, err)
		}
		distinct := map[string]bool{}
		for _, c := range stored {
			in, ok := inputs[c.ID]
			if !ok {
				t.Fatalf("%s: %s has no trust inputs", b.name, c.ID)
			}
			if want := trust.At(in, at); c.TrustScore != want {
				t.Errorf("%s/%s: stored trust %v != trust.At(inputs, at) %v — subsystems would disagree about this belief",
					b.name, c.ID, c.TrustScore, want)
			}
			distinct[fmt.Sprintf("%.6f", c.TrustScore)] = true
		}
		// A corpus whose beliefs all scored the same would satisfy the identity
		// while exercising none of the inputs.
		if len(distinct) < len(shapes)-1 {
			t.Errorf("%s: only %d distinct trust values over %d shapes — the corpus is not exercising the inputs",
				b.name, len(distinct), len(shapes))
		}
	}
}
