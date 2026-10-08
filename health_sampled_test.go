package mnemos

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"
)

// healthBrain opens a passive brain on dsn and seeds every shape a vital
// distinguishes: live and retired (forgotten, deprecated) beliefs, stale and
// recently verified ones, low and high confidence, with and without evidence,
// contradictions between live beliefs and between a live and a retired one,
// and edges to a missing belief.
func healthBrain(t *testing.T, dsn string) (m *memory, dangling int) {
	t.Helper()
	for _, k := range []string{"MNEMOS_STORAGE", "MNEMOS_MODE", "MNEMOS_LLM_PROVIDER", "MNEMOS_API_KEY"} {
		t.Setenv(k, "")
	}
	mem, err := New(WithStorage(dsn), WithPassiveMode())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	m = mem.(*memory)
	ctx := context.Background()
	now := time.Now().UTC()
	day := 24 * time.Hour

	var claims []domain.Claim
	var links []domain.ClaimEvidence
	for i := 0; i < 160; i++ {
		age := time.Duration(i%7) * 60 * day // 0..360 days old: some past the staleness horizon
		c := domain.Claim{
			ID: fmt.Sprintf("h-%03d", i), Text: fmt.Sprintf("belief %d about subsystem %d", i, i%9),
			Type: domain.ClaimTypeFact, Confidence: 0.2 + 0.8*float64(i%10)/10,
			Status: domain.ClaimStatusActive, CreatedAt: now.Add(-age), ValidFrom: now.Add(-age),
		}
		switch {
		case i%11 == 0:
			c.Status = domain.ClaimStatusDeprecated
		case i%13 == 0:
			c.ValidTo = now.Add(-day) // forgotten
		case i%5 == 0:
			c.LastVerified = now.Add(-2 * day)
		}
		if i < 40 && (i%6 == 1 || i == 26) {
			// Half the contradiction pairs below have a promoted side (h-025
			// and h-026 both, a tie to break); the rest qualify, or not, on trust.
			c.Lifecycle = domain.ClaimLifecyclePromoted
		}
		claims = append(claims, c)
		if i%6 != 0 { // every sixth belief is an orphan
			ev := fmt.Sprintf("ev-%03d", i)
			if err := m.conn.Events.Append(ctx, domain.Event{ID: ev, Content: c.Text, SchemaVersion: "v1",
				SourceInputID: "src-" + ev, Timestamp: now.Add(-age), IngestedAt: now.Add(-age), CreatedBy: fmt.Sprintf("u%d", i%3)}); err != nil {
				t.Fatal(err)
			}
			links = append(links, domain.ClaimEvidence{ClaimID: c.ID, EventID: ev})
		}
	}
	if err := m.conn.Claims.Upsert(ctx, claims); err != nil {
		t.Fatal(err)
	}
	// Closed validity goes through SetValidity: the SQL backends' Upsert does
	// not write valid_to, so without this the forgotten beliefs stay live there.
	for _, c := range claims {
		if !c.ValidTo.IsZero() {
			if err := m.conn.Claims.SetValidity(ctx, c.ID, c.ValidTo); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := m.conn.Claims.UpsertEvidence(ctx, links); err != nil {
		t.Fatal(err)
	}
	var rels []domain.Relationship
	for i := 1; i < 40; i += 3 {
		rels = append(rels, domain.Relationship{ID: fmt.Sprintf("x-%d", i), Type: domain.RelationshipTypeContradicts,
			FromClaimID: fmt.Sprintf("h-%03d", i), ToClaimID: fmt.Sprintf("h-%03d", i+1), CreatedAt: now})
	}
	if err := m.conn.Relationships.Upsert(ctx, rels); err != nil {
		t.Fatal(err)
	}
	// Stored trust in {0.5, 0.7, 0.9}, through the real recompute path, so
	// hypercorrections qualify on trust as well as promotion, including at
	// exactly the 0.7 floor. A superseded side resolves its pair.
	scorer, ok := m.conn.Claims.(ports.TrustScorer)
	if !ok {
		t.Fatal("store cannot score trust")
	}
	if _, err := scorer.RecomputeTrust(ctx, domain.TrustScoring{At: now, ModelVersion: "test", Score: func(in domain.TrustInput) float64 {
		return []float64{0.5, 0.7, 0.9}[int(math.Round(in.Confidence*10))%3]
	}}); err != nil {
		t.Fatal(err)
	}
	if err := m.conn.Claims.SetLifecycle(ctx, "h-020", domain.ClaimLifecycleSuperseded); err != nil {
		t.Fatal(err)
	}
	// Edges to a missing belief. SQLite's foreign keys refuse them, so there a
	// brain can only hold them from legacy data; the memory store accepts them.
	for _, g := range []domain.Relationship{
		{ID: "ghost-1", Type: domain.RelationshipTypeSupports, FromClaimID: "h-002", ToClaimID: "missing-a", CreatedAt: now},
		{ID: "ghost-2", Type: domain.RelationshipTypeSupports, FromClaimID: "missing-b", ToClaimID: "h-003", CreatedAt: now},
	} {
		if err := m.conn.Relationships.Upsert(ctx, []domain.Relationship{g}); err == nil {
			dangling++
		}
	}
	return m, dangling
}

func healthBackends(t *testing.T) map[string]string {
	return map[string]string{
		"memory": "memory://?namespace=health_sampled",
		"sqlite": "sqlite://" + filepath.Join(t.TempDir(), "health.db"),
	}
}

func setHealthSampling(t *testing.T, exactBelow, sampleSize int) {
	t.Helper()
	prevBelow, prevSize := healthExactBelow, healthSampleSize
	healthExactBelow, healthSampleSize = exactBelow, sampleSize
	t.Cleanup(func() { healthExactBelow, healthSampleSize = prevBelow, prevSize })
}

// The sampled path is the exact path when its sample covers every live
// belief: same vitals, values, statuses, details and pathologies. The two share
// one tally and one report builder, and the store's SQL counts (live beliefs,
// orphans, dangling edges) must agree with the Go definitions the exact path
// applies. A disagreement in any of them shows up here.
func TestBrainHealth_SampledEqualsExactWhenTheSampleIsEverything(t *testing.T) {
	for name, dsn := range healthBackends(t) {
		t.Run(name, func(t *testing.T) {
			m, dangling := healthBrain(t, dsn)
			ctx := context.Background()
			setHealthSampling(t, 0, 1_000_000)
			sampled, err := m.BrainHealth(ctx)
			if err != nil {
				t.Fatal(err)
			}
			exact, err := m.BrainHealthFull(ctx)
			if err != nil {
				t.Fatal(err)
			}
			sampled.At, exact.At = time.Time{}, time.Time{}
			if !reflect.DeepEqual(sampled, exact) {
				t.Fatalf("sampled path over every belief differs from the exact path:\nsampled: %+v\nexact:   %+v", sampled, exact)
			}
			if exact.Mode != HealthModeExact || exact.Estimates != nil {
				t.Errorf("exact report mode=%q estimates=%v", exact.Mode, exact.Estimates)
			}
			// The brain must exercise what is being compared.
			if p := pathologyByKind(exact, "orphan_claims"); p.Count == 0 {
				t.Error("no orphans seeded")
			}
			if p := pathologyByKind(exact, "dangling_edges"); p.Count != dangling {
				t.Errorf("dangling_edges = %d, want the %d seeded", p.Count, dangling)
			}
			if v := vitalByName(exact, "dissonance"); v.Value == 0 {
				t.Error("no live hypercorrection seeded; dissonance is not being compared")
			}
			if v := vitalByName(exact, "staleness"); v.Value == 0 || v.Value == 1 {
				t.Errorf("staleness %v does not distinguish anything", v.Value)
			}
		})
	}
}

// Above the threshold the default report samples: rate vitals carry an
// estimate, counts stay exact, and the full report is still available.
func TestBrainHealth_SamplesAboveTheThreshold(t *testing.T) {
	for name, dsn := range healthBackends(t) {
		t.Run(name, func(t *testing.T) {
			m, _ := healthBrain(t, dsn)
			ctx := context.Background()
			setHealthSampling(t, 10, 40)
			h, err := m.BrainHealth(ctx)
			if err != nil {
				t.Fatal(err)
			}
			full, err := m.BrainHealthFull(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if h.Mode != HealthModeSampled {
				t.Fatalf("mode = %q, want sampled", h.Mode)
			}
			live := 0
			all, _ := m.conn.Claims.ListAll(ctx)
			for _, c := range all {
				if isLiveBelief(c) {
					live++
				}
			}
			for _, name := range []string{"low_trust", "staleness"} {
				e, ok := h.Estimates[name]
				if !ok || e.SampleSize != 40 || e.Population != live || e.Margin95 <= 0 {
					t.Errorf("%s estimate = %+v (ok=%v), want sample 40 of %d with a margin", name, e, ok, live)
				}
				if got, want := vitalByName(h, name).Value, vitalByName(full, name).Value; math.Abs(got-want) > 0.35 {
					t.Errorf("%s estimate %.3f is nowhere near the exact %.3f", name, got, want)
				}
			}
			for _, kind := range []string{"orphan_claims", "dangling_edges"} {
				if got, want := pathologyByKind(h, kind).Count, pathologyByKind(full, kind).Count; got != want {
					t.Errorf("%s = %d sampled, %d exact; counts must never be estimated", kind, got, want)
				}
			}
			if got, want := vitalByName(h, "dissonance").Value, vitalByName(full, "dissonance").Value; got != want {
				t.Errorf("dissonance %v sampled, %v exact; it is not a sampled vital", got, want)
			}
			again, err := m.BrainHealth(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if vitalByName(again, "low_trust").Value != vitalByName(h, "low_trust").Value {
				t.Error("two health checks of an unchanged brain disagree; the sample must be deterministic")
			}
		})
	}
}

// The margin the report states holds: over many samples of a known
// population, the true rate falls within ±Margin95 of the estimate about 95%
// of the time. A margin that is too tight would make every sampled verdict
// overconfident.
func TestMargin95_HoldsItsCoverage(t *testing.T) {
	const population, positives, n, trials = 5000, 1100, 300, 2000
	ids := make([]string, population)
	positive := map[string]bool{}
	for i := range ids {
		ids[i] = fmt.Sprintf("c%05d", i)
		positive[ids[i]] = i%(population/positives) == 0 && len(positive) < positives
	}
	truth := 0
	for _, p := range positive {
		if p {
			truth++
		}
	}
	p := float64(truth) / population
	covered := 0
	for seed := uint64(0); seed < trials; seed++ {
		k := 0
		for _, id := range store.ChooseSample(ids, n, seed) {
			if positive[id] {
				k++
			}
		}
		if math.Abs(float64(k)/n-p) <= margin95(k, n, population) {
			covered++
		}
	}
	if rate := float64(covered) / trials; rate < 0.93 {
		t.Fatalf("95%% margin covered the true rate in %.1f%% of samples", 100*rate)
	}
}

// The store's hypercorrection count equals hypercorrectionList at the
// boundaries the rule turns on: a contradicted side trusted exactly at the
// floor counts, one just below does not.
func TestHealthSampler_CountHypercorrectionsAtTheFloor(t *testing.T) {
	for name, dsn := range healthBackends(t) {
		t.Run(name, func(t *testing.T) {
			for _, k := range []string{"MNEMOS_STORAGE", "MNEMOS_MODE", "MNEMOS_LLM_PROVIDER", "MNEMOS_API_KEY"} {
				t.Setenv(k, "")
			}
			mem, err := New(WithStorage(dsn), WithPassiveMode())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = mem.Close() })
			m := mem.(*memory)
			ctx := context.Background()
			now := time.Now().UTC()
			// Confidence keys the trust the scorer below assigns.
			trustFor := map[float64]float64{0.11: hypercorrectionTrustFloor, 0.12: hypercorrectionTrustFloor - 0.01, 0.13: 0.5}
			var claims []domain.Claim
			for id, conf := range map[string]float64{"at": 0.11, "at2": 0.11, "below": 0.12, "below2": 0.12, "low1": 0.13, "low2": 0.13, "low3": 0.13, "low4": 0.13} {
				claims = append(claims, domain.Claim{ID: id, Text: "claim " + id, Type: domain.ClaimTypeFact, Confidence: conf,
					Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now})
			}
			if err := m.conn.Claims.Upsert(ctx, claims); err != nil {
				t.Fatal(err)
			}
			if _, err := m.conn.Claims.(ports.TrustScorer).RecomputeTrust(ctx, domain.TrustScoring{At: now, ModelVersion: "test",
				Score: func(in domain.TrustInput) float64 { return trustFor[in.Confidence] }}); err != nil {
				t.Fatal(err)
			}
			if err := m.conn.Relationships.Upsert(ctx, []domain.Relationship{
				{ID: "r-at", Type: domain.RelationshipTypeContradicts, FromClaimID: "low1", ToClaimID: "at", CreatedAt: now},
				{ID: "r-below", Type: domain.RelationshipTypeContradicts, FromClaimID: "low2", ToClaimID: "below", CreatedAt: now},
				// The same two cases with the established side as the edge's source.
				{ID: "r-at2", Type: domain.RelationshipTypeContradicts, FromClaimID: "at2", ToClaimID: "low3", CreatedAt: now},
				{ID: "r-below2", Type: domain.RelationshipTypeContradicts, FromClaimID: "below2", ToClaimID: "low4", CreatedAt: now},
			}); err != nil {
				t.Fatal(err)
			}
			list, err := m.hypercorrectionList(ctx, m.newHealthCorpus())
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, h := range list {
				got[h.ContradictedClaimID] = true
			}
			if len(list) != 2 || !got["at"] || !got["at2"] {
				t.Fatalf("hypercorrectionList = %+v, want exactly the two pairs contradicting a claim at the floor", list)
			}
			n, err := m.conn.Claims.(ports.HealthSampler).CountHypercorrections(ctx, hypercorrectionTrustFloor)
			if err != nil {
				t.Fatal(err)
			}
			if n != len(list) {
				t.Fatalf("CountHypercorrections = %d, hypercorrectionList has %d", n, len(list))
			}
		})
	}
}
