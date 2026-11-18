package query

import (
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// A reassertion row (from_status == to_status) is narrated as a decision that
// confirmed the status, never as the claim "becoming" what it already was.
func TestFormatNarrative_NarratesAReassertionAsAConfirmation(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC) }
	hist := []domain.ClaimStatusTransition{
		{ToStatus: domain.ClaimStatusActive, ChangedAt: day(1)},
		{FromStatus: domain.ClaimStatusActive, ToStatus: domain.ClaimStatusDeprecated, ChangedAt: day(2), Reason: "retracted"},
		{FromStatus: domain.ClaimStatusDeprecated, ToStatus: domain.ClaimStatusDeprecated, ChangedAt: day(3), Reason: "reviewed: wrong"},
	}
	want := "First recorded as active on 2026-11-01; became deprecated on 2026-11-02 (retracted); " +
		"confirmed as deprecated on 2026-11-03 (reviewed: wrong)."
	if got := formatNarrative(hist); got != want {
		t.Errorf("narrative =\n  %s\nwant\n  %s", got, want)
	}
	if got, want := formatNarrative(hist[2:]), "Confirmed as deprecated on 2026-11-03 (reviewed: wrong)."; got != want {
		t.Errorf("narrative = %q, want %q", got, want)
	}
}
