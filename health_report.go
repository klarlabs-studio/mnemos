package mnemos

import (
	"context"
	"fmt"
	"math"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/trust"
)

// Health modes, reported in BrainHealth.Mode.
const (
	// HealthModeExact: every vital computed over every belief.
	HealthModeExact = "exact"
	// HealthModeSampled: counts exact, per-belief rate vitals estimated from a
	// sample (see BrainHealth.Estimates).
	HealthModeSampled = "sampled"
)

// healthExactBelow is the live-belief count at or below which BrainHealth
// computes everything exactly. Above it, a full scan costs seconds (a 1M-belief
// brain decodes every row twice: ~13 s warm, minutes cold), so the rate vitals
// are estimated from healthSampleSize beliefs instead. A variable so tests can
// exercise the sampled path on a small brain.
var healthExactBelow = 50_000

// healthSampleSize is the sample the rate vitals are estimated from. At 20k
// the 95% margin on a rate is at most about ±0.7 percentage points.
var healthSampleSize = 20_000

// healthSampleSeed fixes the sample, so two health checks of an unchanged brain
// agree and a change in a vital means the brain changed, not the dice.
const healthSampleSeed uint64 = 0x6d6e656d6f73

// BrainHealth implements [Memory.BrainHealth] (ADR 0019): the unified
// read-only health verdict. On a brain with more than healthExactBelow live
// beliefs, the per-belief rate vitals (low_trust, staleness, trust_decay) are
// estimated from a fixed sample and reported with their margin in Estimates;
// every count (live beliefs, orphans, dangling edges, contradictions,
// calibration) stays exact. [Memory.BrainHealthFull] always scans everything.
func (m *memory) BrainHealth(ctx context.Context) (BrainHealth, error) {
	if s, ok := m.conn.Claims.(ports.HealthSampler); ok {
		live, err := s.CountLiveClaims(ctx)
		if err != nil {
			return BrainHealth{}, fmt.Errorf("mnemos: BrainHealth: count live beliefs: %w", err)
		}
		if live > healthExactBelow {
			return m.sampledBrainHealth(ctx, s, live)
		}
	}
	return m.exactBrainHealth(ctx)
}

// BrainHealthFull implements [Memory.BrainHealthFull]: BrainHealth computed
// exactly over every belief, whatever the brain's size.
func (m *memory) BrainHealthFull(ctx context.Context) (BrainHealth, error) {
	return m.exactBrainHealth(ctx)
}

// beliefTally is what the per-belief vitals are computed from: how many
// currently-valid beliefs were assessed and how many of them are low-trust,
// stale, trusted and decaying. The exact path tallies every live belief; the
// sampled path tallies a sample. Both feed the same report builder, so the
// two can only differ in what they counted, never in how it is graded.
type beliefTally struct {
	valid, lowTrust, stale, trusted, decaying int
}

// assess adds one currently-valid belief to the tally. in/hasInputs are its
// canonical trust inputs (ADR 0026); without them the stored score stands in.
func (t *beliefTally) assess(c domain.Claim, in domain.TrustInput, hasInputs bool, now, decayHorizon time.Time) {
	t.valid++
	// Canonical trust now, not the stored cache: the cache was computed at
	// trust_computed_at and freshness has decayed since.
	trustNow := c.TrustScore
	if hasInputs {
		trustNow = trust.At(in, now)
	}
	if trustNow < healthLowTrustFloor {
		t.lowTrust++
	}
	ref := c.CreatedAt
	if c.LastVerified.After(ref) {
		ref = c.LastVerified
	}
	if now.Sub(ref).Hours()/24 > healthStalenessHorizonDays {
		t.stale++
	}
	if !hasInputs {
		return
	}
	// trust_decay reads the SAME beliefs forward: how many of those trusted
	// today stop being trusted within the horizon if nobody re-verifies them.
	if tr, d := projectTrustDecay(in, now, decayHorizon); tr {
		t.trusted++
		if d {
			t.decaying++
		}
	}
}

func decayHorizonFrom(now time.Time) time.Time {
	return now.Add(time.Duration(healthTrustDecayHorizonDays * 24 * float64(time.Hour)))
}

// exactBrainHealth computes every vital over every belief.
func (m *memory) exactBrainHealth(ctx context.Context) (BrainHealth, error) {
	now := time.Now().UTC()
	// One snapshot for every vital and check below (healthCorpus).
	h := m.newHealthCorpus()
	claims, err := h.Claims(ctx)
	if err != nil {
		return BrainHealth{}, fmt.Errorf("mnemos: BrainHealth: list claims: %w", err)
	}
	evidence, err := m.conn.Claims.ListAllEvidence(ctx)
	if err != nil {
		return BrainHealth{}, fmt.Errorf("mnemos: BrainHealth: list evidence: %w", err)
	}
	evidenceCount := make(map[string]int, len(claims))
	for _, e := range evidence {
		evidenceCount[e.ClaimID]++
	}
	// Canonical trust inputs for every claim (ADR 0026), so low_trust and
	// trust_decay evaluate trust.At — the same function the stored score caches.
	trustInputs := map[string]domain.TrustInput{}
	if lister, ok := m.conn.Claims.(ports.TrustInputLister); ok {
		ins, terr := lister.ListTrustInputs(ctx, nil)
		if terr != nil {
			return BrainHealth{}, fmt.Errorf("mnemos: BrainHealth: trust inputs: %w", terr)
		}
		trustInputs = ins
	}
	claimIDs := make(map[string]struct{}, len(claims))
	var tally beliefTally
	orphans := 0
	decayHorizon := decayHorizonFrom(now)
	for _, c := range claims {
		claimIDs[c.ID] = struct{}{} // every claim, so dangling-edge detection sees them all
		// Forgotten (valid time closed) and deprecated beliefs are not
		// currently valid: counting a deprecated belief inflated every vital
		// and kept an orphan warning alive that deprecating it should clear.
		if !isLiveBelief(c) {
			continue
		}
		in, hasInputs := trustInputs[c.ID]
		tally.assess(c, in, hasInputs, now, decayHorizon)
		if evidenceCount[c.ID] == 0 {
			orphans++ // a claim requires evidence — an orphan is a data-integrity smell
		}
	}
	rels, err := h.Relationships(ctx)
	if err != nil {
		return BrainHealth{}, fmt.Errorf("mnemos: BrainHealth: list relationships: %w", err)
	}
	dangling := 0
	for _, r := range rels {
		if _, ok := claimIDs[r.FromClaimID]; !ok {
			dangling++
			continue
		}
		if _, ok := claimIDs[r.ToClaimID]; !ok {
			dangling++
		}
	}
	return m.healthReport(ctx, h, now, tally, nil, orphans, dangling)
}

// sampledBrainHealth estimates the per-belief rate vitals from a fixed sample
// of live beliefs and computes everything else exactly, through the store's
// counts. When the sample covers every live belief it is the exact report.
func (m *memory) sampledBrainHealth(ctx context.Context, s ports.HealthSampler, live int) (BrainHealth, error) {
	now := time.Now().UTC()
	h := m.newHealthCorpus()
	h.liveCount = live
	h.sampler = s
	sample, err := s.SampleLiveClaims(ctx, healthSampleSize, healthSampleSeed)
	if err != nil {
		return BrainHealth{}, fmt.Errorf("mnemos: BrainHealth: sample beliefs: %w", err)
	}
	trustInputs := map[string]domain.TrustInput{}
	if lister, ok := m.conn.Claims.(ports.TrustInputLister); ok {
		ids := make([]string, len(sample))
		for i, c := range sample {
			ids[i] = c.ID
		}
		ins, terr := lister.ListTrustInputs(ctx, ids)
		if terr != nil {
			return BrainHealth{}, fmt.Errorf("mnemos: BrainHealth: trust inputs: %w", terr)
		}
		trustInputs = ins
	}
	var tally beliefTally
	decayHorizon := decayHorizonFrom(now)
	for _, c := range sample {
		in, hasInputs := trustInputs[c.ID]
		tally.assess(c, in, hasInputs, now, decayHorizon)
	}
	orphans, err := s.CountLiveOrphans(ctx)
	if err != nil {
		return BrainHealth{}, fmt.Errorf("mnemos: BrainHealth: count orphans: %w", err)
	}
	dangling, err := s.CountDanglingRelationships(ctx)
	if err != nil {
		return BrainHealth{}, fmt.Errorf("mnemos: BrainHealth: count dangling edges: %w", err)
	}
	var est *healthSample
	if tally.valid < live {
		est = &healthSample{population: live}
	}
	return m.healthReport(ctx, h, now, tally, est, orphans, dangling)
}

// healthSample marks a tally as a sample of population live beliefs.
type healthSample struct{ population int }

// margin95 is the half-width of the 95% Wilson interval for k of n, with the
// finite-population correction for a sample of n out of population.
func margin95(k, n, population int) float64 {
	if n == 0 {
		return 0
	}
	const z = 1.96
	p := float64(k) / float64(n)
	nf := float64(n)
	half := z / (1 + z*z/nf) * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf))
	if population > 1 && n < population {
		half *= math.Sqrt(float64(population-n) / float64(population-1))
	}
	return half
}

// healthReport grades a tally and the exact checks into the verdict. Both
// paths end here; sample is nil when the tally covers every live belief.
func (m *memory) healthReport(ctx context.Context, h *healthCorpus, now time.Time, t beliefTally, sample *healthSample, orphans, dangling int) (BrainHealth, error) {
	pe, err := m.predictiveErrorIn(ctx, h)
	if err != nil {
		return BrainHealth{}, fmt.Errorf("mnemos: BrainHealth: predictive error: %w", err)
	}
	cal, err := h.Calibration(ctx)
	if err != nil {
		return BrainHealth{}, fmt.Errorf("mnemos: BrainHealth: calibration: %w", err)
	}
	// Reuse the PredictiveError dissonance level (active hypercorrections per belief).
	dissonance, dissonanceSamples := 0.0, 0
	for _, l := range pe.Levels {
		if l.Level == "dissonance" {
			dissonance, dissonanceSamples = l.Error, l.Samples
		}
	}
	rate := func(n int) float64 {
		if t.valid == 0 {
			return 0
		}
		return float64(n) / float64(t.valid)
	}
	lowTrustRate, stalenessRate := rate(t.lowTrust), rate(t.stale)

	population := t.valid
	countPhrase := func(k int) string { return fmt.Sprintf("%d/%d", k, t.valid) }
	var estimates map[string]VitalEstimate
	mode := HealthModeExact
	if sample != nil {
		mode = HealthModeSampled
		population = sample.population
		countPhrase = func(k int) string {
			return fmt.Sprintf("~%.1f%% of %d (sample of %d)", 100*float64(k)/float64(max(t.valid, 1)), population, t.valid)
		}
		estimates = map[string]VitalEstimate{
			"low_trust": {SampleSize: t.valid, Population: population, Margin95: margin95(t.lowTrust, t.valid, population)},
			"staleness": {SampleSize: t.valid, Population: population, Margin95: margin95(t.stale, t.valid, population)},
		}
		if t.trusted > 0 {
			// trust_decay is a rate among the trusted part of the sample.
			estimates["trust_decay"] = VitalEstimate{SampleSize: t.trusted, Population: population,
				Margin95: margin95(t.decaying, t.trusted, max(population*t.trusted/max(t.valid, 1), t.trusted))}
		}
	}

	vitals := []Vital{
		{"free_energy", pe.Total, gradeWithSamples(pe.LevelsMeasured, pe.Total, healthFreeEnergyWarn, healthFreeEnergyCrit),
			fmt.Sprintf("overall prediction-error aggregate; most wrong at: %s", orNone(pe.Hotspot))},
		{"calibration", cal.ECE, gradeWithSamples(cal.Samples, cal.ECE, healthCalibrationWarn, healthCalibrationCrit),
			fmt.Sprintf("expected calibration error over %d adjudicated belief(s)", cal.Samples)},
		{"dissonance", dissonance, gradeWithSamples(dissonanceSamples, dissonance, healthDissonanceWarn, healthDissonanceCrit),
			"active high-stakes contradictions per belief"},
		{"low_trust", lowTrustRate, gradeWithSamples(t.valid, lowTrustRate, healthLowTrustWarn, healthLowTrustCrit),
			fmt.Sprintf("%s valid beliefs below trust %.2f", countPhrase(t.lowTrust), healthLowTrustFloor)},
		{"staleness", stalenessRate, gradeWithSamples(t.valid, stalenessRate, healthStalenessWarn, healthStalenessCrit),
			fmt.Sprintf("%s valid beliefs unverified in %.0f days", countPhrase(t.stale), healthStalenessHorizonDays)},
		trustDecayVital(t.trusted, t.decaying),
		m.skillCoverageVital(ctx),
	}

	// --- Pathologies (integrity checks that did not exist before) ---
	staleExpectations := 0
	if m.conn.Expectations != nil {
		open, oerr := m.conn.Expectations.ListOpen(ctx)
		if oerr != nil {
			return BrainHealth{}, fmt.Errorf("mnemos: BrainHealth: list open expectations: %w", oerr)
		}
		for _, exp := range open {
			if !exp.Horizon.IsZero() && exp.Horizon.Before(now) {
				staleExpectations++
			}
		}
	}
	orphanStatus := HealthOK
	if orphans > 0 {
		orphanStatus = HealthDegraded
	}
	danglingStatus := HealthOK
	if dangling > 0 {
		danglingStatus = HealthUnhealthy // referential corruption
	}
	staleExpStatus := HealthOK
	if staleExpectations >= healthStaleExpectationCrit {
		staleExpStatus = HealthUnhealthy
	} else if staleExpectations > 0 {
		staleExpStatus = HealthDegraded
	}
	pathologies := []Pathology{
		{"orphan_claims", orphans, orphanStatus, "currently-valid beliefs with zero evidence"},
		{"dangling_edges", dangling, danglingStatus, "relationships whose endpoint belief is missing"},
		{"stale_expectations", staleExpectations, staleExpStatus, "open predictions past their horizon (unreconciled)"},
	}
	// --- Overall verdict: worst of everything ---
	overall := HealthOK
	for _, v := range vitals {
		overall = worseHealth(overall, v.Status)
	}
	for _, p := range pathologies {
		overall = worseHealth(overall, p.Status)
	}
	return BrainHealth{Status: overall, Mode: mode, Vitals: vitals, Pathologies: pathologies, Estimates: estimates, At: now}, nil
}
