package brainbench

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

var identityNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

// belief is one claim plus its canonical trust inputs, aged from identityNow.
func belief(id string, ageDays float64) (domain.Claim, domain.TrustInput) {
	at := identityNow.Add(-time.Duration(ageDays * 24 * float64(time.Hour)))
	return domain.Claim{ID: id, Status: domain.ClaimStatusActive, CreatedAt: at, ValidFrom: at},
		domain.TrustInput{Confidence: 0.8, EvidenceCount: 1, LatestEvidence: at}
}

// arm assembles a census arm from (id, age) pairs; merged maps a loser id to
// its survivor and turns the loser into the deprecated redirect a merge leaves.
type armBelief struct {
	id  string
	age float64
}

func buildArm(beliefs []armBelief, merged map[string]string) ([]domain.Claim, map[string]domain.TrustInput) {
	claims := make([]domain.Claim, 0, len(beliefs))
	inputs := make(map[string]domain.TrustInput, len(beliefs))
	for _, b := range beliefs {
		c, in := belief(b.id, b.age)
		if into, ok := merged[b.id]; ok {
			c.Status = domain.ClaimStatusDeprecated
			c.MergedInto = into
		}
		claims = append(claims, c)
		inputs[b.id] = in
	}
	return claims, inputs
}

// background is enough distinct, not-yet-crossing beliefs for the ratio to be
// measurable (each contributes ~0.28 expected crossings), plus one belief that
// genuinely reaches the floor this month.
var background = []armBelief{
	{"a", 10}, {"b", 15}, {"c", 20}, {"d", 25}, {"crossing", 90},
}

func withBackground(extra ...armBelief) []armBelief {
	return append(append([]armBelief{}, background...), extra...)
}

func knownValue(t *testing.T, m Metric) float64 {
	t.Helper()
	if !m.Known {
		t.Fatalf("%s unknown (%s); the fixture must be measurable", m.Name, m.Unknown)
	}
	return m.Value
}

// TestIdentityTrustDecay_CorrectMergeIsNeutral is the defect this metric
// exists to fix. A fresh restatement and its older original are one belief.
// Counted as two, the untreated arm carries an extra not-yet-crossing belief,
// so folding the duplicate away shrinks the expected crossings and inflates
// the ratio — the merge is scored as decay it did not cause. By identity, both
// arms hold one belief with the same freshest reference, so the metric must
// not move, whichever of the pair the merge kept.
func TestIdentityTrustDecay_CorrectMergeIsNeutral(t *testing.T) {
	for _, tc := range []struct {
		name            string
		survivor, loser string
	}{
		{"fresh restatement survives", "fresh", "old"},
		{"older original survives and inherits the evidence", "old", "fresh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids := Identity{tc.loser: tc.survivor}
			pair := []armBelief{{"old", 40}, {"fresh", 5}}

			cClaims, cInputs := buildArm(withBackground(pair...), nil)
			control := identityTrustDecay(cClaims, cInputs, ids, identityNow)

			// A correct merge leaves the survivor with the union of both claims'
			// evidence, so its freshest evidence is the restatement's.
			tClaims, tInputs := buildArm(withBackground(pair...), map[string]string{tc.loser: tc.survivor})
			tInputs[tc.survivor] = cInputs["fresh"]
			treatment := identityTrustDecay(tClaims, tInputs, ids, identityNow)

			if c, tr := knownValue(t, control), knownValue(t, treatment); c != tr {
				t.Fatalf("identity trust decay moved %.4f -> %.4f across a correct merge; a merge changes no belief's freshness", c, tr)
			}

			// The counting artefact is real: by claim, the same merge reads worse.
			byClaim := identityTrustDecay(cClaims, cInputs, nil, identityNow)
			if knownValue(t, byClaim) >= knownValue(t, treatment) {
				t.Fatalf("precondition: per-claim counting (%.4f) should read better than the merged arm (%.4f)",
					byClaim.Value, treatment.Value)
			}
		})
	}
}

// TestIdentityTrustDecay_RealDecayStillRegisters pins that identity counting
// cannot hide a genuine loss of freshness. Two ways a brain really decays:
// a merge that keeps the stale claim and drops the restatement's evidence, and
// a belief untouched by any merge that is simply staler after the process.
func TestIdentityTrustDecay_RealDecayStillRegisters(t *testing.T) {
	t.Run("merge that loses the fresh evidence", func(t *testing.T) {
		ids := Identity{"fresh": "old"}
		pair := []armBelief{{"old", 100}, {"fresh", 5}}
		cClaims, cInputs := buildArm(withBackground(pair...), nil)
		tClaims, tInputs := buildArm(withBackground(pair...), map[string]string{"fresh": "old"})
		control := knownValue(t, identityTrustDecay(cClaims, cInputs, ids, identityNow))
		treatment := knownValue(t, identityTrustDecay(tClaims, tInputs, ids, identityNow))
		if treatment <= control {
			t.Fatalf("identity trust decay %.4f -> %.4f: a merge that kept a belief reaching the floor and dropped its fresh restatement must read worse",
				control, treatment)
		}
	})
	t.Run("unmerged belief aged onto its way to the floor", func(t *testing.T) {
		cClaims, cInputs := buildArm(withBackground(armBelief{"x", 10}), nil)
		tClaims, tInputs := buildArm(withBackground(armBelief{"x", 95}), nil)
		control := knownValue(t, identityTrustDecay(cClaims, cInputs, nil, identityNow))
		treatment := knownValue(t, identityTrustDecay(tClaims, tInputs, nil, identityNow))
		if treatment <= control {
			t.Fatalf("identity trust decay %.4f -> %.4f: a belief now crossing the floor must read worse", control, treatment)
		}
	})
}

// TestIdentityTrustDecay_UnmeasurableIsUnknown keeps the harness's rule: too
// few beliefs on their way to the floor is unknown, never a healthy 0.
func TestIdentityTrustDecay_UnmeasurableIsUnknown(t *testing.T) {
	claims, inputs := buildArm([]armBelief{{"only", 10}}, nil)
	m := identityTrustDecay(claims, inputs, nil, identityNow)
	if m.Known {
		t.Fatalf("one approaching belief measured as %.4f; want unknown", m.Value)
	}
	if m.Direction != LowerBetter {
		t.Fatalf("direction = %q, want %q", m.Direction, LowerBetter)
	}
}

// TestIdentity_FollowsRedirectChains: a survivor may itself have been merged
// since, so the identity of a claim is the end of its redirect chain.
func TestIdentity_FollowsRedirectChains(t *testing.T) {
	ids := identityFrom([]domain.Claim{
		{ID: "a", MergedInto: "b"},
		{ID: "b", MergedInto: "c"},
		{ID: "c"},
		{ID: "loop1", MergedInto: "loop2"},
		{ID: "loop2", MergedInto: "loop1"},
	})
	if got := ids.canonical("a"); got != "c" {
		t.Fatalf("canonical(a) = %q, want c", got)
	}
	if got := ids.canonical("c"); got != "c" {
		t.Fatalf("canonical(c) = %q, want c", got)
	}
	// A cycle cannot be resolved; it must terminate rather than hang.
	_ = ids.canonical("loop1")
}

// TestRun_MergeOfFreshDuplicateDoesNotRegressTrustDecay is the end-to-end
// version: consolidation folds a fresh restatement into an older belief on a
// real brain. The ADR 0019 vital, which counts claims, moves; the scored
// identity metric must not, and the run must not be reported as a regression.
func TestRun_MergeOfFreshDuplicateDoesNotRegressTrustDecay(t *testing.T) {
	sc := Scenario{
		ID: "merge-decay",
		Corpus: []Doc{
			{ID: "orig", AgeDays: 40, Text: "The billing service runs on PostgreSQL 16."},
			{ID: "restated", AgeDays: 5, Text: "The billing service runs on PostgreSQL 16 today."},
			{ID: "crossing", AgeDays: 90, Text: "Reindexing runs nightly at 02:00 UTC."},
			{ID: "d1", AgeDays: 10, Text: "Redis is the cache layer in front of the checkout service."},
			{ID: "d2", AgeDays: 15, Text: "The search service uses Elasticsearch 8 for the product index."},
			{ID: "d3", AgeDays: 20, Text: "Connection pooling is handled by pgbouncer in transaction mode."},
			{ID: "d4", AgeDays: 25, Text: "The cache TTL is sixty seconds for every tenant."},
		},
		Probes: []Probe{
			{ID: "db", Query: "which database does the billing service run on", Expect: "PostgreSQL 16"},
		},
		Process: Process{DedupeThreshold: 0.92},
	}
	res, err := Run(context.Background(), sc, t.TempDir())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Activity.Merged == 0 {
		t.Fatal("precondition failed: consolidation merged nothing, so this run tests nothing")
	}
	if !res.Validity.Deterministic {
		t.Fatalf("measurement not deterministic: %v", res.Validity.NondeterministicMetrics)
	}

	byIdentity, ok := comparisonNamed(res, MetricTrustDecayByIdentity)
	if !ok {
		t.Fatalf("%s not measured", MetricTrustDecayByIdentity)
	}
	if byIdentity.Verdict == VerdictWorse || byIdentity.Verdict == VerdictUnknown {
		t.Fatalf("%s verdict = %q (control %v, treatment %v): a correct merge must not read as decay",
			MetricTrustDecayByIdentity, byIdentity.Verdict, fmtMetric(byIdentity.Control), fmtMetric(byIdentity.Treatment))
	}

	vital, ok := comparisonNamed(res, MetricVitalPrefix+"trust_decay")
	if !ok {
		t.Fatal("vital_trust_decay must still be reported")
	}
	t.Logf("vital_trust_decay %s -> %s (%s); %s %s -> %s (%s)",
		fmtMetric(vital.Control), fmtMetric(vital.Treatment), vital.Verdict, MetricTrustDecayByIdentity,
		fmtMetric(byIdentity.Control), fmtMetric(byIdentity.Treatment), byIdentity.Verdict)
	if !vital.Control.Known || !vital.Treatment.Known || vital.Treatment.Value <= vital.Control.Value {
		t.Fatal("precondition failed: the per-claim vital did not rise across the merge, so the defect is not exercised")
	}
	if vital.Direction != Descriptive {
		t.Fatalf("vital_trust_decay direction = %q, want %q: it counts a merged duplicate as a separate belief",
			vital.Direction, Descriptive)
	}
	if res.Verdict == VerdictRegressed || res.Verdict == VerdictMixed {
		t.Fatalf("verdict = %q for a run whose only mutation was a correct merge", res.Verdict)
	}
}
