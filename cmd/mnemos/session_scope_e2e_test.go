package main

import (
	"context"
	"testing"
)

// End-to-end through the real capture path: two process_text calls under ONE
// session id must not produce a contradiction between the narrative's
// beginning and its end, while the same two statements from DIFFERENT sessions
// still do.
//
// This covers the wiring the unit tests cannot: that capture's session id
// reaches the event metadata, and that the filter runs where relationships are
// assembled.
func TestE2E_SessionScopedCapture(t *testing.T) {
	ctx := context.Background()

	first := "The capture hook is broken and does not persist anything."
	second := "The capture hook works now and persists every chunk."

	for _, tc := range []struct {
		name    string
		session string
		wantGT0 bool // want contradictions > 0
	}{
		{"same session", "sess-A", false},
		{"different sessions", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := t.TempDir()
			t.Setenv("MNEMOS_DB_URL", "sqlite://"+d+"/x.db")
			s1, s2 := tc.session, tc.session
			if tc.session == "" {
				s1, s2 = "sess-X", "sess-Y"
			}
			if _, err := mcpRunProcessText(ctx, "t", mcpProcessTextInput{Text: first, SessionID: s1}); err != nil {
				t.Fatal(err)
			}
			out, err := mcpRunProcessText(ctx, "t", mcpProcessTextInput{Text: second, SessionID: s2})
			if err != nil {
				t.Fatal(err)
			}
			conn, err := openConn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			rels, err := conn.Relationships.ListAll(ctx)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, r := range rels {
				if r.Type == "contradicts" {
					n++
				}
			}
			t.Logf("session(%s,%s) claims=%d contradicts=%d", s1, s2, out.Claims, n)
			if tc.wantGT0 && n == 0 {
				t.Errorf("cross-session contradiction must survive, got 0")
			}
			if !tc.wantGT0 && n != 0 {
				t.Errorf("same-session contradiction must be suppressed, got %d", n)
			}
		})
	}
}
