package domain

import "testing"

func TestEffectiveEvidenceCount(t *testing.T) {
	// EvidenceRepeatWeight = 0 (#443): only independent origins count.
	cases := []struct {
		name            string
		distinct, total int
		want            int
	}{
		{"no evidence", 0, 0, 0},
		{"one source one event", 1, 1, 1},
		{"one source many events (echo chamber)", 1, 5, 1},
		{"five independent sources", 5, 5, 5},
		{"mixed: 2 sources, 3 events", 2, 3, 2},
		{"mixed: 2 sources, 4 events", 2, 4, 2},
		{"garbage: total < distinct", 3, 1, 3}, // clamped
		{"negative distinct", -2, 4, 0},
	}
	for _, c := range cases {
		if got := EffectiveEvidenceCount(c.distinct, c.total); got != c.want {
			t.Errorf("%s: EffectiveEvidenceCount(%d,%d) = %d, want %d", c.name, c.distinct, c.total, got, c.want)
		}
	}

	// Five events from ONE origin corroborate strictly less than five events
	// from FIVE origins...
	if EffectiveEvidenceCount(1, 5) >= EffectiveEvidenceCount(5, 5) {
		t.Fatal("one voice repeated must corroborate less than five independent voices")
	}
	// ...and exactly as much as one event: a repeat adds provenance only.
	if EffectiveEvidenceCount(1, 5) != EffectiveEvidenceCount(1, 1) {
		t.Fatal("a repeat from an origin already counted must not add corroboration")
	}
}

func TestEvidenceOrigin(t *testing.T) {
	meta := func(kv ...string) map[string]string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	tests := []struct {
		name string
		a, b Event
		same bool
	}{
		{"one file, two principals",
			Event{CreatedBy: "alice", Content: "x", Metadata: meta("input_source_path", "/a.md")},
			Event{CreatedBy: "bob", Content: "y", Metadata: meta("input_source_path", "/a.md")}, true},
		{"two files, identical text",
			Event{Content: "x", Metadata: meta("input_source_path", "/a.md")},
			Event{Content: "x", Metadata: meta("input_source_path", "/b.md")}, false},
		{"a path outranks the session",
			Event{Content: "x", Metadata: meta("input_source_path", "/a.md", "session_id", "s1")},
			Event{Content: "y", Metadata: meta("input_source_path", "/a.md", "session_id", "s2")}, true},
		{"one session, two messages",
			Event{Content: "x", Metadata: meta("session_id", "s1")},
			Event{Content: "y", Metadata: meta("session_id", "s1")}, true},
		{"the legacy session key names the same session",
			Event{Content: "x", Metadata: meta("session_id", "s1")},
			Event{Content: "y", Metadata: meta("input_session_id", "s1")}, true},
		{"two sessions, identical text",
			Event{Content: "x", Metadata: meta("session_id", "s1")},
			Event{Content: "x", Metadata: meta("session_id", "s2")}, false},
		{"identical text across inputs, ASCII case and spaces ignored",
			Event{SourceInputID: "in1", CreatedBy: "alice", Content: "Hello World"},
			Event{SourceInputID: "in2", CreatedBy: "bob", Content: "  hello world "}, true},
		{"distinct texts", Event{Content: "hello"}, Event{Content: "goodbye"}, false},
		{"empty path and session fall through to the text",
			Event{Content: "x", Metadata: meta("input_source_path", "", "session_id", "")},
			Event{Content: "X"}, true},
		{"a path never equals a text that spells it",
			Event{Content: "/a.md"},
			Event{Content: "q", Metadata: meta("input_source_path", "/a.md")}, false},
		{"a session never equals a path that spells it",
			Event{Content: "q", Metadata: meta("session_id", "/a.md")},
			Event{Content: "q", Metadata: meta("input_source_path", "/a.md")}, false},
		{"only U+0020 is trimmed, as SQL TRIM does", Event{Content: "x"}, Event{Content: "\tx"}, false},
		{"only ASCII letters are folded, as SQLite lower() does", Event{Content: "Ä"}, Event{Content: "ä"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oa, ob := EvidenceOrigin(tt.a), EvidenceOrigin(tt.b)
			if got := oa == ob; got != tt.same {
				t.Errorf("same origin = %v, want %v (%q vs %q)", got, tt.same, oa, ob)
			}
		})
	}
}
