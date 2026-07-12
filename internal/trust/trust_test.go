package trust

import (
	"math"
	"testing"
	"time"
)

func TestScore_ConfidenceFloorAndCeiling(t *testing.T) {
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)

	if got := Score(-0.5, 1, now, now); got < 0 || got > 1 {
		t.Fatalf("negative confidence should clamp; got %v", got)
	}
	if got := Score(2.0, 1, now, now); got < 0 || got > 1 {
		t.Fatalf(">1 confidence should clamp; got %v", got)
	}
	if got := Score(0, 1, now, now); got != 0 {
		t.Fatalf("zero confidence should score zero; got %v", got)
	}
}

func TestScore_SingleFreshSourceMatchesConfidence(t *testing.T) {
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	got := Score(0.9, 1, now, now)
	// 1 source → corroboration = 1.0; same-day → freshness = 1.0
	if math.Abs(got-0.9) > 1e-9 {
		t.Fatalf("single fresh source: got %v, want 0.9", got)
	}
}

func TestScore_CorroborationBoosts(t *testing.T) {
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	one := Score(0.7, 1, now, now)
	five := Score(0.7, 5, now, now)
	twenty := Score(0.7, 20, now, now)

	if !(five > one) {
		t.Fatalf("5 sources should outscore 1; got %v vs %v", five, one)
	}
	if !(twenty > five) {
		t.Fatalf("20 sources should outscore 5; got %v vs %v", twenty, five)
	}
	// Logarithmic shape: gap 1→5 should be larger than 5→20.
	if (five - one) <= (twenty - five) {
		t.Fatalf("expected diminishing returns: 1→5 gap %v should exceed 5→20 gap %v", five-one, twenty-five)
	}
}

func TestScore_FreshnessDecays(t *testing.T) {
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	fresh := Score(0.9, 1, now, now)
	month := Score(0.9, 1, now.Add(-30*24*time.Hour), now)
	year := Score(0.9, 1, now.Add(-365*24*time.Hour), now)

	if !(fresh > month) {
		t.Fatalf("month-old should be lower than fresh; got fresh=%v month=%v", fresh, month)
	}
	if !(month > year) {
		t.Fatalf("year-old should be lower than month-old; got month=%v year=%v", month, year)
	}
	// Floor: even the year-old claim must stay above FreshnessFloor*confidence.
	if year < 0.9*FreshnessFloor {
		t.Fatalf("year-old should not drop below floor*confidence (%v); got %v", 0.9*FreshnessFloor, year)
	}
}

func TestScore_FreshnessFloorIsRespected(t *testing.T) {
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	// 10 years old, but Paris is still the capital of France.
	old := Score(0.95, 1, now.Add(-10*365*24*time.Hour), now)
	if old < 0.95*FreshnessFloor-1e-9 {
		t.Fatalf("ancient evidence should not collapse below floor; got %v", old)
	}
}

func TestScore_ZeroLatestEvidenceDisablesFreshness(t *testing.T) {
	// Backfill case: callers that haven't loaded evidence yet pass
	// the zero timestamp; we should not punish them with stale-decay.
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	got := Score(0.8, 3, time.Time{}, now)
	want := 0.8 * (1 + math.Log(3)*CorroborationCoefficient)
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("zero timestamp should disable freshness; got %v want %v", got, want)
	}
}

func TestScore_FutureTimestampActsAsFresh(t *testing.T) {
	// Clock skew or a manually-stamped event in the future shouldn't
	// produce an out-of-range score.
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	got := Score(0.9, 1, now.Add(7*24*time.Hour), now)
	if got != 0.9 {
		t.Fatalf("future timestamp should score as fresh; got %v want 0.9", got)
	}
}

func TestScore_StayInUnitInterval(t *testing.T) {
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		confidence float64
		evCount    int
		evAge      time.Duration
	}{
		{0.95, 100, 0},
		{0.99, 1000, -1 * 24 * time.Hour},
		{0.5, 500, 0},
		{0.0, 1, 1000 * 24 * time.Hour},
	}
	for _, c := range cases {
		got := Score(c.confidence, c.evCount, now.Add(-c.evAge), now)
		if got < 0 || got > 1 {
			t.Fatalf("trust %v out of [0,1] for case %+v", got, c)
		}
	}
}

func TestScoreWithHalfLife_DefaultsWhenZero(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	latest := now.AddDate(0, -1, 0)
	a := Score(0.9, 3, latest, now)
	b := ScoreWithHalfLife(0.9, 3, latest, now, 0)
	if math.Abs(a-b) > 1e-9 {
		t.Fatalf("zero half-life should match Score: %v vs %v", a, b)
	}
}

func TestScoreWithHalfLife_CustomDecays(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	latest := now.AddDate(0, 0, -7)
	short := ScoreWithHalfLife(0.9, 3, latest, now, 7)
	long := ScoreWithHalfLife(0.9, 3, latest, now, 90)
	if !(short < long) {
		t.Fatalf("shorter half-life should produce lower score: short=%v long=%v", short, long)
	}
}

func TestIsStale_FreshClaim(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if IsStale(now, time.Time{}, now, 0, 0) {
		t.Fatal("zero-age claim must not be stale")
	}
}

func TestIsStale_AgedClaimWithDefaultHalfLife(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(-2, 0, 0)
	if !IsStale(old, time.Time{}, now, 0, 0) {
		t.Fatalf("two-year-old claim with default half-life should be stale")
	}
}

func TestIsStale_LastVerifiedRescuesFreshness(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(-2, 0, 0)
	verified := now.AddDate(0, 0, -1)
	if IsStale(old, verified, now, 0, 0) {
		t.Fatalf("recent verification should refresh freshness even with old evidence")
	}
}

func TestIsStale_PerClaimHalfLifeShortensDecay(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	latest := now.AddDate(0, 0, -10)
	if IsStale(latest, time.Time{}, now, 0, 0) {
		t.Fatal("default half-life should not flag a 10-day-old claim as stale")
	}
	if !IsStale(latest, time.Time{}, now, 5, 0) {
		t.Fatal("5-day half-life should flag a 10-day-old claim as stale")
	}
}

func TestStaleness_ZeroRefIsFresh(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if got := Staleness(time.Time{}, time.Time{}, now, 0); got != 0 {
		t.Fatalf("no reference timestamp should yield 0 staleness, got %.4f", got)
	}
}

func TestStaleness_GrowsWithAge(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	fresh := Staleness(now.AddDate(0, 0, -1), time.Time{}, now, 0)
	old := Staleness(now.AddDate(0, 0, -180), time.Time{}, now, 0)
	if !(old > fresh) {
		t.Fatalf("older claim must be more stale: old=%.4f fresh=%.4f", old, fresh)
	}
	if old < 0 || old > 1 {
		t.Fatalf("staleness must stay in [0,1], got %.4f", old)
	}
}

func TestStaleness_LastVerifiedRescues(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -180)
	verified := now.AddDate(0, 0, -1)
	// A recent verification resets the reference, so staleness drops.
	if s := Staleness(old, verified, now, 0); s > Staleness(old, time.Time{}, now, 0) {
		t.Fatalf("a recent last-verified must reduce staleness, got %.4f", s)
	}
}

func TestScoreWithAuthority_ZeroAuthorityIsNeutral(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	base := Score(0.8, 5, now, now)
	got := ScoreWithAuthority(0.8, 5, now, now, 0)
	if got != base {
		t.Fatalf("zero agentAuthority should not change score: base=%.4f got=%.4f", base, got)
	}
}

func TestScoreWithAuthority_HighAuthorityPreservesScore(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	base := Score(0.8, 5, now, now)
	got := ScoreWithAuthority(0.8, 5, now, now, 1.0)
	if math.Abs(got-base) > 1e-9 {
		t.Fatalf("authority=1.0 should preserve score: base=%.4f got=%.4f", base, got)
	}
}

func TestScoreWithAuthority_LowAuthorityDeflatesScore(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	base := Score(0.8, 5, now, now)
	got := ScoreWithAuthority(0.8, 5, now, now, 0.5)
	if got >= base {
		t.Fatalf("low authority should deflate score: base=%.4f got=%.4f", base, got)
	}
	// deflated should be approximately base * 0.5
	if math.Abs(got-base*0.5) > 1e-9 {
		t.Fatalf("authority=0.5 should halve the score: want %.4f got %.4f", base*0.5, got)
	}
}

func TestScoreWithAuthority_ClampedToOne(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	got := ScoreWithAuthority(2.0, 1, now, now, 2.0)
	if got > 1.0 {
		t.Fatalf("score must not exceed 1.0; got %v", got)
	}
}
