package trust

import (
	"math"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func agedInput(now time.Time, ageDays float64) domain.TrustInput {
	return domain.TrustInput{
		Confidence:     0.8,
		EvidenceCount:  1,
		LatestEvidence: now.Add(-time.Duration(ageDays * 24 * float64(time.Hour))),
	}
}

func TestProjectDecay(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	floorAge := FreshnessHalfLifeDays * math.Log(1/FreshnessFloor) // ~108 days
	expected := DecayHorizonDays / floorAge

	tests := []struct {
		name                  string
		in                    domain.TrustInput
		approaching, crossing bool
		expected              float64
	}{
		{"undated cannot be projected", domain.TrustInput{Confidence: 0.8, EvidenceCount: 1}, false, false, 0},
		{"fresh belief approaches but does not cross", agedInput(now, 10), true, false, expected},
		{"belief reaching the floor within the horizon crosses", agedInput(now, floorAge-10), true, true, expected},
		{"belief already on the floor belongs to staleness", agedInput(now, floorAge+1), false, false, 0},
		{"low-trust on its merits is excluded", domain.TrustInput{
			Confidence: 0.1, EvidenceCount: 1, LatestEvidence: now.Add(-24 * time.Hour),
		}, false, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, c, e := ProjectDecay(tc.in, now)
			if a != tc.approaching || c != tc.crossing || math.Abs(e-tc.expected) > 1e-9 {
				t.Fatalf("ProjectDecay = (%v, %v, %.4f), want (%v, %v, %.4f)", a, c, e, tc.approaching, tc.crossing, tc.expected)
			}
		})
	}
}

func TestDecayTally_UnmeasurableBelowMinExpected(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	var tally DecayTally
	tally.Add(agedInput(now, 100), now) // one crossing against ~0.28 expected
	if _, ok := tally.Ratio(); ok {
		t.Fatalf("ratio over %.2f expected crossings must be unmeasurable, got measurable", tally.Expected)
	}
	for i := 0; i < 4; i++ {
		tally.Add(agedInput(now, 10), now)
	}
	ratio, ok := tally.Ratio()
	if !ok {
		t.Fatalf("five approaching beliefs (%.2f expected) must be measurable", tally.Expected)
	}
	if want := 1 / tally.Expected; math.Abs(ratio-want) > 1e-9 {
		t.Fatalf("ratio = %.4f, want crossing/expected = %.4f", ratio, want)
	}
}
