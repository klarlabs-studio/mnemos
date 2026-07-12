package credit

import (
	"math"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestResistanceFor_YoungBeliefIsFullyPlastic(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	c := domain.Claim{CreatedAt: now, VerifyCount: 0} // brand new, never verified
	r := ResistanceFor(c, now)
	if r < 0.999 {
		t.Fatalf("a brand-new belief should be ~fully plastic (r≈1), got %v", r)
	}
}

func TestResistanceFor_OldVerifiedBeliefCrystallizes(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	// Old (created 1yr ago), often-verified, recently confirmed → crystallized.
	c := domain.Claim{
		CreatedAt:    now.AddDate(-1, 0, 0),
		VerifyCount:  30,
		LastVerified: now.AddDate(0, 0, -1),
	}
	r := ResistanceFor(c, now)
	if r >= 0.6 {
		t.Fatalf("an old, often-verified, recently-confirmed belief should resist (r well below 1), got %v", r)
	}
	if r < MinResistance-1e-9 {
		t.Fatalf("resistance must never fall below MinResistance %v, got %v", MinResistance, r)
	}
}

func TestResistanceFor_StaleVerificationReducesCrystallization(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	base := domain.Claim{CreatedAt: now.AddDate(-1, 0, 0), VerifyCount: 30}
	recent := base
	recent.LastVerified = now.AddDate(0, 0, -1)
	stale := base
	stale.LastVerified = now.AddDate(-1, 0, 0) // not re-verified in a year
	if ResistanceFor(stale, now) <= ResistanceFor(recent, now) {
		t.Fatalf("a long-unverified belief should be MORE plastic (higher resistance) than a freshly-verified one: stale=%v recent=%v",
			ResistanceFor(stale, now), ResistanceFor(recent, now))
	}
}

func TestSumForModulated_NeutralEqualsSumFor(t *testing.T) {
	contribs := []Contribution{{Key: "credit:d1:b1", Delta: 0.06}, {Key: "credit:d2:b1", Delta: -0.02}}
	if got, want := SumForModulated(contribs, 1, 1), SumFor(contribs); math.Abs(got-want) > 1e-12 {
		t.Fatalf("resistance=1,gain=1 must equal SumFor: got %v want %v", got, want)
	}
}

func TestSumForModulated_ResistanceDampsCredit(t *testing.T) {
	contribs := []Contribution{{Key: "credit:d1:b1", Delta: 0.08}}
	full := SumForModulated(contribs, 1, 1)
	damped := SumForModulated(contribs, MinResistance, 1)
	if damped >= full {
		t.Fatalf("resistance should damp positive credit: damped=%v full=%v", damped, full)
	}
	if math.Abs(damped-0.08*MinResistance) > 1e-9 {
		t.Fatalf("credit should scale by resistance exactly: got %v want %v", damped, 0.08*MinResistance)
	}
}

func TestSumForModulated_BlameResistedLessThanCredit(t *testing.T) {
	// Same magnitude, opposite sign, same crystallization: blame should retain more
	// of its strength than credit (the disconfirmability asymmetry).
	creditC := []Contribution{{Key: "credit:d1:b1", Delta: 0.08}}
	blameC := []Contribution{{Key: "credit:d1:b1", Delta: -0.08}}
	r := MinResistance
	dampedCredit := SumForModulated(creditC, r, 1)         // positive, ×r
	dampedBlame := math.Abs(SumForModulated(blameC, r, 1)) // negative, ×(r + (1-r)·breakability)
	if dampedBlame <= dampedCredit {
		t.Fatalf("blame should be resisted less than credit: |blame|=%v credit=%v", dampedBlame, dampedCredit)
	}
}

func TestSumForModulated_GainCannotBreachCreditCap(t *testing.T) {
	// A pile of same-sign contributions at maximum gain must still clamp to CreditCap.
	contribs := []Contribution{
		{Key: "a", Delta: 0.2}, {Key: "b", Delta: 0.2}, {Key: "c", Delta: 0.2},
	}
	got := SumForModulated(contribs, 1, MaxGain)
	if got > CreditCap+1e-12 {
		t.Fatalf("gain must not move trust past CreditCap %v, got %v", CreditCap, got)
	}
	if math.Abs(got-CreditCap) > 1e-12 {
		t.Fatalf("saturated sum should sit at CreditCap, got %v", got)
	}
}

func TestSumForModulated_GainOutOfRangeIsClamped(t *testing.T) {
	contribs := []Contribution{{Key: "a", Delta: 0.05}}
	// A wildly out-of-range gain is clamped to [MinGain,MaxGain]; result stays sane.
	got := SumForModulated(contribs, 1, 1000)
	if got > CreditCap+1e-12 {
		t.Fatalf("clamped gain must respect CreditCap, got %v", got)
	}
	if lo := SumForModulated(contribs, 1, -5); lo != SumForModulated(contribs, 1, MinGain) {
		t.Fatalf("negative gain should clamp to MinGain: got %v", lo)
	}
}
