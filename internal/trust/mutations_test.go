package trust

import (
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// The tests in this file are written specifically to kill mutants
// surfaced by the in-tree mutation harness (tools/mutate). Each test
// pins a single boundary or branch that the broader behaviour-oriented
// tests miss. When a mutant survives, the fix is to add the targeted
// assertion here rather than weaken the harness.

// TestBuildReport_BaseTrustContributes pins the `base == 0` defaulting
// branch in credibility.go: when CurrentTrust is unset (0) we substitute
// 0.5; when it's a real value we use it. Inverting the comparison must
// change the score for at least one of these inputs.
func TestBuildReport_BaseTrustContributes(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	zero, _ := ScoreCredibility(CredibilityInputs{CurrentTrust: 0, Liveness: domain.LivenessLive, Now: now})
	high, _ := ScoreCredibility(CredibilityInputs{CurrentTrust: 0.95, Liveness: domain.LivenessLive, Now: now})
	if !(high > zero) {
		t.Fatalf("CurrentTrust=0.95 must outscore CurrentTrust=0; got high=%.4f zero=%.4f", high, zero)
	}
	if (high - zero) < 0.10 {
		t.Fatalf("expected base_trust signal to move score by at least 0.10; got %.4f", high-zero)
	}
	// Pin the absolute level for CurrentTrust=0: the `base == 0` branch
	// must substitute 0.5, contributing wBase*0.5 = 0.25 to the
	// weighted sum. Without that default the mutant `base != 0` would
	// leave base=0 and drop the score below ~0.40.
	if zero < 0.40 {
		t.Fatalf("CurrentTrust=0 must apply 0.5 default → score ≥ 0.40; got %.4f", zero)
	}
}

// TestBuildReport_AuthorityZeroIsNeutralDefault pins the
// `SourceAuthority == 0` branch separately from the parity test: at
// SourceAuthority=0 the contribution must equal the SourceAuthority=0.5
// score within float tolerance, while SourceAuthority=0.95 must move
// the score upward.
func TestBuildReport_AuthorityZeroIsNeutralDefault(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	unset, _ := ScoreCredibility(CredibilityInputs{CurrentTrust: 0.6, SourceAuthority: 0.0, Liveness: domain.LivenessLive, Now: now})
	half, _ := ScoreCredibility(CredibilityInputs{CurrentTrust: 0.6, SourceAuthority: 0.5, Liveness: domain.LivenessLive, Now: now})
	high, _ := ScoreCredibility(CredibilityInputs{CurrentTrust: 0.6, SourceAuthority: 0.95, Liveness: domain.LivenessLive, Now: now})

	if abs(unset-half) > 1e-9 {
		t.Errorf("SourceAuthority=0 must default to 0.5 contribution: unset=%.6f half=%.6f", unset, half)
	}
	if !(high > half) {
		t.Errorf("authority=0.95 must outscore authority=0.5; got high=%.4f half=%.4f", high, half)
	}
}

// TestBuildReport_TestRecencyOnlyAppliesWhenIsTest pins the
// `in.IsTest && !in.TestLastRunAt.IsZero()` AND-guard. The mutant
// flipping `&&` to `||` would route non-test claims with a stray
// TestLastRunAt timestamp through the test-recency path. We assert
// that for IsTest=false, TestLastRunAt is ignored.
func TestBuildReport_TestRecencyOnlyAppliesWhenIsTest(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	withStaleTestRun, _ := ScoreCredibility(CredibilityInputs{
		CurrentTrust:  0.7,
		Liveness:      domain.LivenessLive,
		LastExecuted:  now,
		Now:           now,
		IsTest:        false,
		TestLastRunAt: now.Add(-365 * 24 * time.Hour),
	})
	withoutTestRun, _ := ScoreCredibility(CredibilityInputs{
		CurrentTrust:  0.7,
		Liveness:      domain.LivenessLive,
		LastExecuted:  now,
		Now:           now,
		IsTest:        false,
		TestLastRunAt: time.Time{},
	})
	if abs(withStaleTestRun-withoutTestRun) > 1e-9 {
		t.Fatalf("TestLastRunAt must be ignored when IsTest=false: with=%.6f without=%.6f", withStaleTestRun, withoutTestRun)
	}
}

// TestBuildReport_AgentAuthoritySignalEmittedOnlyWhenNonNeutral pins
// the `agentFactor != 1.0` guard around the agent_authority signal
// append. The `!=` → `==` mutant would emit the signal for the neutral
// (1.0) case and skip it for the deflated case.
func TestBuildReport_AgentAuthoritySignalEmittedOnlyWhenNonNeutral(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	neutral := CredibilityInputs{CurrentTrust: 0.7, Liveness: domain.LivenessLive, Now: now}
	deflated := neutral
	deflated.AgentAuthority = 0.5

	_, neutralSignals, _, _ := BuildReport(neutral)
	for _, s := range neutralSignals {
		if s.Name == "agent_authority" {
			t.Fatalf("neutral agent factor must not append signal; got %+v", neutralSignals)
		}
	}

	_, deflatedSignals, _, _ := BuildReport(deflated)
	found := false
	for _, s := range deflatedSignals {
		if s.Name == "agent_authority" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("non-neutral agent factor must append signal; got %+v", deflatedSignals)
	}
}

// TestBuildReport_SignalsSortedByContributionDescending pins the
// sort comparator. The `>` → `<` mutant flips the order; we assert
// strict non-increasing contribution across the slice.
func TestBuildReport_SignalsSortedByContributionDescending(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	_, signals, _, _ := BuildReport(CredibilityInputs{
		CurrentTrust:    0.9,
		SourceAuthority: 0.9,
		CitationCount:   8,
		Liveness:        domain.LivenessLive,
		LastExecuted:    now.Add(-1 * 24 * time.Hour),
		Now:             now,
	})
	if len(signals) < 2 {
		t.Fatalf("expected ≥2 signals; got %d", len(signals))
	}
	for i := 1; i < len(signals); i++ {
		// agent_authority has Contribution=0 by design, allowed at the tail.
		if signals[i].Name == "agent_authority" {
			continue
		}
		if signals[i].Contribution > signals[i-1].Contribution {
			t.Fatalf("signals[%d].Contribution=%.4f > signals[%d].Contribution=%.4f (should be non-increasing)",
				i, signals[i].Contribution, i-1, signals[i-1].Contribution)
		}
	}
}

// TestProseRationale_NonTestRecencyBuckets pins the three buckets
// in the non-test branch of buildProseRationale: <7 (fresh), <90
// (mid), ≥90 (stale). The `<` → `>` mutants on these comparisons
// route the wrong bucket; we assert the bucket label per case.
func TestProseRationale_NonTestRecencyBuckets(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		exec      time.Time
		mustHave  string
		mustntHas []string
	}{
		{"fresh", now.Add(-2 * 24 * time.Hour), "(fresh)", []string{"(stale)"}},
		{"mid", now.Add(-30 * 24 * time.Hour), "Most recent evidence 30 days ago.", []string{"(fresh)", "(stale)"}},
		{"stale", now.Add(-200 * 24 * time.Hour), "(stale)", []string{"(fresh)"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, prose := BuildReport(CredibilityInputs{
				CurrentTrust: 0.5,
				LastExecuted: c.exec,
				Liveness:     domain.LivenessLive,
				Now:          now,
			})
			if !strings.Contains(prose, c.mustHave) {
				t.Errorf("prose missing %q; got %q", c.mustHave, prose)
			}
			for _, banned := range c.mustntHas {
				if strings.Contains(prose, banned) {
					t.Errorf("prose must not contain %q; got %q", banned, prose)
				}
			}
		})
	}
}

// TestProseRationale_AuthorityBuckets pins the three SourceAuthority
// buckets: ==0 (unset), ≥0.8 (high), <0.3 (low). Mutations on these
// boundaries route the wrong narrative.
func TestProseRationale_AuthorityBuckets(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		auth float64
		want string
	}{
		{"unset", 0.0, "Authority not configured"},
		{"low", 0.2, "Low-authority source"},
		{"high", 0.9, "High-authority source"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, prose := BuildReport(CredibilityInputs{
				CurrentTrust:    0.5,
				SourceAuthority: c.auth,
				Liveness:        domain.LivenessLive,
				Now:             now,
			})
			if !strings.Contains(prose, c.want) {
				t.Errorf("auth=%.2f prose missing %q; got %q", c.auth, c.want, prose)
			}
		})
	}
}

// TestProseRationale_AuthorityBoundaryAt08 pins the `>= 0.8` boundary.
// Mutating to `<= 0.8` would route 0.79 into the high bucket instead.
func TestProseRationale_AuthorityBoundaryAt08(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	_, _, _, just_below := BuildReport(CredibilityInputs{
		CurrentTrust:    0.5,
		SourceAuthority: 0.79,
		Liveness:        domain.LivenessLive,
		Now:             now,
	})
	_, _, _, exactly := BuildReport(CredibilityInputs{
		CurrentTrust:    0.5,
		SourceAuthority: 0.80,
		Liveness:        domain.LivenessLive,
		Now:             now,
	})
	if strings.Contains(just_below, "High-authority source") {
		t.Errorf("0.79 must NOT be high-authority; got %q", just_below)
	}
	if !strings.Contains(exactly, "High-authority source") {
		t.Errorf("0.80 MUST be high-authority; got %q", exactly)
	}
}

// TestProseRationale_AuthorityBoundaryAt03 pins the `< 0.3` low bucket
// boundary against the `<` → `>` mutant. 0.30 is not low; 0.29 is.
func TestProseRationale_AuthorityBoundaryAt03(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	_, _, _, below := BuildReport(CredibilityInputs{
		CurrentTrust:    0.5,
		SourceAuthority: 0.29,
		Liveness:        domain.LivenessLive,
		Now:             now,
	})
	_, _, _, exactly := BuildReport(CredibilityInputs{
		CurrentTrust:    0.5,
		SourceAuthority: 0.30,
		Liveness:        domain.LivenessLive,
		Now:             now,
	})
	if !strings.Contains(below, "Low-authority source") {
		t.Errorf("0.29 MUST be low-authority; got %q", below)
	}
	if strings.Contains(exactly, "Low-authority source") {
		t.Errorf("0.30 must NOT be low-authority; got %q", exactly)
	}
}

// TestProseRationale_CitationBuckets pins the citation buckets:
// 0 (none, no message), 1-4 ("citation(s)"), ≥5 ("citations").
func TestProseRationale_CitationBuckets(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		count     int
		mustHave  string
		mustntHas string
	}{
		{"none", 0, "", "Corroborated by"},
		{"few", 3, "Corroborated by 3 citation(s)", ""},
		{"many", 5, "Corroborated by 5 citations.", ""},
		{"many2", 9, "Corroborated by 9 citations.", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, prose := BuildReport(CredibilityInputs{
				CurrentTrust:  0.5,
				CitationCount: c.count,
				Liveness:      domain.LivenessLive,
				Now:           now,
			})
			if c.mustHave != "" && !strings.Contains(prose, c.mustHave) {
				t.Errorf("count=%d prose missing %q; got %q", c.count, c.mustHave, prose)
			}
			if c.mustntHas != "" && strings.Contains(prose, c.mustntHas) {
				t.Errorf("count=%d prose must not contain %q; got %q", c.count, c.mustntHas, prose)
			}
		})
	}
}

// TestProseRationale_TestRecencyOnlyAppliesWhenIsTest pins the
// `in.IsTest && !in.TestLastRunAt.IsZero()` AND-guard inside
// buildProseRationale (separate from the BuildReport-level guard
// covered by TestBuildReport_TestRecencyOnlyAppliesWhenIsTest).
// Mutating `&&` to `||` would surface "Last ran ..." prose for
// non-test claims that happen to carry a TestLastRunAt timestamp,
// or for test claims with a zero timestamp.
func TestProseRationale_TestRecencyOnlyAppliesWhenIsTest(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	// IsTest=true but TestLastRunAt zero: must NOT emit "Last ran".
	_, _, _, testNoRunAt := BuildReport(CredibilityInputs{
		CurrentTrust: 0.5,
		Liveness:     domain.LivenessLive,
		Now:          now,
		IsTest:       true,
	})
	if strings.Contains(testNoRunAt, "Last ran") {
		t.Errorf("test with zero TestLastRunAt must not emit 'Last ran'; got %q", testNoRunAt)
	}

	// IsTest=false with stray TestLastRunAt: must NOT emit "Last ran".
	_, _, _, nonTestWithRunAt := BuildReport(CredibilityInputs{
		CurrentTrust:  0.5,
		Liveness:      domain.LivenessLive,
		Now:           now,
		IsTest:        false,
		TestLastRunAt: now.Add(-1 * time.Hour),
	})
	if strings.Contains(nonTestWithRunAt, "Last ran") {
		t.Errorf("non-test with TestLastRunAt must not emit 'Last ran'; got %q", nonTestWithRunAt)
	}
}

// TestProseRationale_AgentAuthorityReducedNote pins the `agentFactor
// < 1.0` branch in the prose. With a reduced factor we must surface
// the note; with neutral 1.0 we must not.
func TestProseRationale_AgentAuthorityReducedNote(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	_, _, _, reduced := BuildReport(CredibilityInputs{
		CurrentTrust:   0.5,
		Liveness:       domain.LivenessLive,
		AgentAuthority: 0.4,
		Now:            now,
	})
	_, _, _, full := BuildReport(CredibilityInputs{
		CurrentTrust:   0.5,
		Liveness:       domain.LivenessLive,
		AgentAuthority: 1.0,
		Now:            now,
	})
	if !strings.Contains(reduced, "Submitting agent has reduced authority") {
		t.Errorf("reduced factor must surface note; got %q", reduced)
	}
	if strings.Contains(full, "Submitting agent has reduced authority") {
		t.Errorf("full authority must not surface reduced note; got %q", full)
	}
}

// TestRecencyDetail_NegativeDaysClamped pins the `days < 0` clamp in
// recencyDetail. A future-dated reference yields `0 days` not a
// negative number; the mutated `days > 0` would skip the clamp.
func TestRecencyDetail_NegativeDaysClamped(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	_, signals, _, _ := BuildReport(CredibilityInputs{
		CurrentTrust: 0.5,
		LastExecuted: now.Add(48 * time.Hour), // future
		Liveness:     domain.LivenessLive,
		Now:          now,
	})
	for _, s := range signals {
		if s.Name == "recency" {
			if !strings.Contains(s.Detail, "0 days since last evidence") {
				t.Fatalf("future timestamp must clamp to 0 days; got %q", s.Detail)
			}
			return
		}
	}
	t.Fatal("recency signal not present")
}

// TestMaxIntComparator pins the `a > b` comparator inside maxInt.
// Surfaced through BuildReport: positive CitationCount must boost
// the citation contribution above the zero baseline. Inverting the
// comparator would make maxInt(0, 5) return 0, collapsing the
// boost. We use a positive count so the original/mutant divergence
// is observable through a real (non-NaN) score difference.
func TestMaxIntComparator(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	withCit, _ := ScoreCredibility(CredibilityInputs{
		CurrentTrust:  0.5,
		CitationCount: 5,
		Liveness:      domain.LivenessLive,
		Now:           now,
	})
	withZero, _ := ScoreCredibility(CredibilityInputs{
		CurrentTrust:  0.5,
		CitationCount: 0,
		Liveness:      domain.LivenessLive,
		Now:           now,
	})
	if !(withCit > withZero) {
		t.Fatalf("positive citations must boost score: with=%.6f zero=%.6f", withCit, withZero)
	}
}

// TestIsStale_ThresholdDefaultBoundary pins the `threshold <= 0`
// default-fallback boundary in IsStale. threshold=0 falls back to
// FreshnessFloor; threshold=0.99 (extreme but positive) does not.
func TestIsStale_ThresholdDefaultBoundary(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(0, -2, 0) // 2-month-old, around half-life
	defaultStale := IsStale(old, time.Time{}, now, 0, 0)
	tightStale := IsStale(old, time.Time{}, now, 0, 0.99)
	if defaultStale {
		t.Fatalf("threshold=0 (default %.2f) should NOT flag 2-month-old as stale", FreshnessFloor)
	}
	if !tightStale {
		t.Fatalf("threshold=0.99 should flag 2-month-old as stale")
	}
}

// TestIsStale_LastVerifiedOnlyWhenAfter pins the AND-guard on the
// LastVerified rescue path. With `&&`, the rescue applies only when
// lastVerified is non-zero AND strictly after the evidence ref.
// Mutating `&&` to `||` would clobber a recent evidence ref with an
// older verification timestamp, flipping a fresh claim to stale.
func TestIsStale_LastVerifiedOnlyWhenAfter(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	// Fresh evidence (10 days, well within default half-life) plus an
	// older lastVerified (1 year). The original AND-guard skips the
	// rescue (After is false), keeping the fresh ref. The `||` mutant
	// would overwrite ref with the year-old verification, dragging
	// freshness below the floor and flagging the claim stale.
	freshEvidence := now.AddDate(0, 0, -10)
	olderVerified := now.AddDate(-1, 0, 0)
	if IsStale(freshEvidence, olderVerified, now, 0, 0) {
		t.Fatalf("older LastVerified must not overwrite fresh evidence: ref=%v verified=%v",
			freshEvidence, olderVerified)
	}

	// Two-year-old evidence with zero LastVerified: rescue cannot fire.
	if !IsStale(now.AddDate(-2, 0, 0), time.Time{}, now, 0, 0) {
		t.Fatalf("zero LastVerified must not rescue old evidence")
	}
}

// TestLivenessWeight_BoundariesPerStatus pins the per-status return
// values in livenessWeight via the recency-independent contribution
// of the liveness signal in BuildReport.
func TestLivenessWeight_BoundariesPerStatus(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	contrib := func(s domain.LivenessStatus) float64 {
		_, signals, _, _ := BuildReport(CredibilityInputs{
			CurrentTrust: 0.5, Liveness: s, Now: now, LastExecuted: now,
		})
		for _, sig := range signals {
			if sig.Name == "liveness" {
				return sig.Value
			}
		}
		t.Fatalf("liveness signal missing for %s", s)
		return 0
	}
	live := contrib(domain.LivenessLive)
	stale := contrib(domain.LivenessStale)
	zombie := contrib(domain.LivenessZombie)
	dead := contrib(domain.LivenessDead)
	if !(live > stale && stale > zombie && zombie > dead) {
		t.Fatalf("liveness ordering broken: live=%.2f stale=%.2f zombie=%.2f dead=%.2f", live, stale, zombie, dead)
	}
}
