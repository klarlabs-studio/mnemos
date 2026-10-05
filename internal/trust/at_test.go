package trust

import (
	"math"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/credit"
	"go.klarlabs.de/mnemos/internal/domain"
)

var atNow = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func daysAgo(d float64) time.Time { return atNow.Add(-time.Duration(d * 24 * float64(time.Hour))) }

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

// A belief with no time constant of its own, never verified and never
// credited must score exactly as the pre-ADR-0026 formula did. This is the
// behavioural-equivalence half of the change: only beliefs that carry one of
// the new inputs may move.
func TestAt_EqualsScoreForDefaultInputs(t *testing.T) {
	for _, conf := range []float64{0, 0.3, 0.8, 1, 1.4} {
		for _, n := range []int{0, 1, 3, 40} {
			for _, age := range []float64{-5, 0, 1, 30, 90, 400, 5000} {
				latest := daysAgo(age)
				got := At(domain.TrustInput{Confidence: conf, EvidenceCount: n, LatestEvidence: latest}, atNow)
				want := Score(conf, n, latest, atNow)
				if !near(got, want) {
					t.Fatalf("conf=%v n=%d age=%v: At=%v Score=%v", conf, n, age, got, want)
				}
			}
		}
	}
	if got, want := At(domain.TrustInput{Confidence: 0.7}, atNow), Score(0.7, 0, time.Time{}, atNow); !near(got, want) {
		t.Fatalf("undated: At=%v Score=%v", got, want)
	}
}

// The worked example of ADR 0026: one stored value, not three.
func TestAt_HonoursThePerBeliefTimeConstant(t *testing.T) {
	in := domain.TrustInput{Confidence: 0.9, EvidenceCount: 1, LatestEvidence: daysAgo(30), HalfLifeDays: 14}
	got := At(in, atNow)
	if want := ScoreWithHalfLife(0.9, 1, daysAgo(30), atNow, 14); !near(got, want) {
		t.Fatalf("At=%v, ScoreWithHalfLife=%v; the per-belief time constant must drive freshness", got, want)
	}
	if global := Score(0.9, 1, daysAgo(30), atNow); near(got, global) {
		t.Fatalf("At=%v equals the global-constant score %v; HalfLifeDays was ignored", got, global)
	}
}

// Explicit confirmation is a freshness reference: a belief confirmed yesterday
// is fresh even when its evidence is a year old.
func TestAt_LastConfirmedRefreshesFreshness(t *testing.T) {
	old := domain.TrustInput{Confidence: 0.9, EvidenceCount: 1, LatestEvidence: daysAgo(365)}
	verified := old
	verified.LastConfirmed = daysAgo(1)
	stale, fresh := At(old, atNow), At(verified, atNow)
	if !near(stale, 0.9*FreshnessFloor) {
		t.Fatalf("unconfirmed year-old belief = %v, want the floor %v", stale, 0.9*FreshnessFloor)
	}
	if want := 0.9 * math.Exp(-1.0/FreshnessHalfLifeDays); !near(fresh, want) {
		t.Fatalf("confirmed yesterday = %v, want %v", fresh, want)
	}
	// An OLDER verification than the evidence must not age the belief.
	older := old
	older.LatestEvidence = daysAgo(2)
	older.LastConfirmed = daysAgo(200)
	if got, want := At(older, atNow), Score(0.9, 1, daysAgo(2), atNow); !near(got, want) {
		t.Fatalf("stale confirmation aged a fresh belief: %v vs %v", got, want)
	}
}

func TestAt_AddsAppliedCreditWithinTheCap(t *testing.T) {
	base := domain.TrustInput{Confidence: 0.5, EvidenceCount: 1, LatestEvidence: atNow}
	for _, tc := range []struct {
		credit, want float64
	}{
		{0.1, 0.6},
		{-0.2, 0.3},
		{5, 0.5 + credit.CreditCap},  // capped
		{-5, 0.5 - credit.CreditCap}, // capped
	} {
		in := base
		in.Credit = tc.credit
		if got := At(in, atNow); !near(got, tc.want) {
			t.Errorf("credit %v: At=%v, want %v", tc.credit, got, tc.want)
		}
	}
	high := domain.TrustInput{Confidence: 1, EvidenceCount: 50, LatestEvidence: atNow, Credit: 0.3}
	low := domain.TrustInput{Confidence: 0.05, EvidenceCount: 1, LatestEvidence: atNow, Credit: -0.3}
	if got := At(high, atNow); got != 1 {
		t.Errorf("At above 1: %v", got)
	}
	if got := At(low, atNow); got != 0 {
		t.Errorf("At below 0: %v", got)
	}
}

func TestScorer_BindsOneInstant(t *testing.T) {
	in := domain.TrustInput{Confidence: 0.8, EvidenceCount: 2, LatestEvidence: daysAgo(45), HalfLifeDays: 30}
	if got, want := Scorer(atNow).Score(in), At(in, atNow); got != want {
		t.Fatalf("Scorer(at).Score(in)=%v, At(in, at)=%v", got, want)
	}
	if s := Scorer(atNow); !s.At.Equal(atNow) || s.ModelVersion != ModelVersion {
		t.Fatalf("Scorer stamps At=%v version=%q, want %v %q", s.At, s.ModelVersion, atNow, ModelVersion)
	}
}
