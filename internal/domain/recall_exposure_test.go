package domain

import (
	"testing"
	"time"
)

func TestRecallSeeded(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	exposures := []RecallExposure{
		{SessionID: "s-recalled", ClaimID: "c1", ExposedAt: at},
		{SessionID: "s-other-belief", ClaimID: "c2", ExposedAt: at},
	}
	ep := func(meta map[string]string, ts time.Time) Event {
		return Event{Content: "x", Metadata: meta, Timestamp: ts}
	}
	tests := []struct {
		name string
		ev   Event
		want bool
	}{
		{"said after the session was shown the belief", ep(map[string]string{"session_id": "s-recalled"}, at.Add(time.Minute)), true},
		{"said at the instant of exposure", ep(map[string]string{"session_id": "s-recalled"}, at), true},
		{"legacy session key", ep(map[string]string{"input_session_id": "s-recalled"}, at.Add(time.Minute)), true},
		{"said before the session was shown it", ep(map[string]string{"session_id": "s-recalled"}, at.Add(-time.Minute)), false},
		{"a session that was never shown it", ep(map[string]string{"session_id": "s-fresh"}, at.Add(time.Minute)), false},
		{"the session was shown a different belief", ep(map[string]string{"session_id": "s-other-belief"}, at.Add(time.Minute)), false},
		{"a file read in the session is the file's evidence", ep(map[string]string{"session_id": "s-recalled", "input_source_path": "/a.md"}, at.Add(time.Minute)), false},
		{"no session at all", ep(nil, at.Add(time.Minute)), false},
	}
	for _, tc := range tests {
		if got := RecallSeeded(tc.ev, "c1", exposures); got != tc.want {
			t.Errorf("%s: RecallSeeded = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRecallExposureValidate(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if err := (RecallExposure{SessionID: "s", ClaimID: "c", ExposedAt: at}).Validate(); err != nil {
		t.Fatalf("valid exposure rejected: %v", err)
	}
	for _, bad := range []RecallExposure{
		{ClaimID: "c", ExposedAt: at},
		{SessionID: "s", ExposedAt: at},
		{SessionID: "s", ClaimID: "c"},
	} {
		if bad.Validate() == nil {
			t.Errorf("invalid exposure %+v accepted", bad)
		}
	}
}
