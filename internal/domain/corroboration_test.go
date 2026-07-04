package domain

import "testing"

func TestEffectiveEvidenceCount(t *testing.T) {
	// Wants below assume EvidenceRepeatWeight = 0.5 (repeats floored).
	cases := []struct {
		name            string
		distinct, total int
		want            int
	}{
		{"no evidence", 0, 0, 0},
		{"one source one event", 1, 1, 1},
		{"one source many events (echo chamber)", 1, 5, 3}, // 1 + floor(0.5*4) = 3, not 5
		{"five independent sources", 5, 5, 5},              // full credit
		{"mixed: 2 sources, 3 events", 2, 3, 2},            // 2 + floor(0.5*1) = 2
		{"mixed: 2 sources, 4 events", 2, 4, 3},            // 2 + floor(0.5*2) = 3
		{"garbage: total < distinct", 3, 1, 3},             // clamped
		{"negative distinct", -2, 4, 2},                    // distinct→0, floor(0.5*4) = 2
	}
	for _, c := range cases {
		if got := EffectiveEvidenceCount(c.distinct, c.total); got != c.want {
			t.Errorf("%s: EffectiveEvidenceCount(%d,%d) = %d, want %d", c.name, c.distinct, c.total, got, c.want)
		}
	}

	// The core guarantee: five events from ONE author corroborate strictly less
	// than five events from FIVE authors.
	if EffectiveEvidenceCount(1, 5) >= EffectiveEvidenceCount(5, 5) {
		t.Fatal("one voice repeated must corroborate less than five independent voices")
	}
	// And more than a single event (the repeat isn't worthless).
	if EffectiveEvidenceCount(1, 5) <= EffectiveEvidenceCount(1, 1) {
		t.Fatal("repeated evidence should still count for something")
	}
}
