package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSpanTime(t *testing.T) {
	cases := []struct {
		name string
		span string
		want time.Time
	}{
		{"first conversational line wins",
			`{"type":"summary","timestamp":"2023-06-30T09:00:00Z"}` + "\n" +
				`{"type":"user","timestamp":"2023-07-03T10:15:00Z","message":{"role":"user","content":"hi"}}` + "\n" +
				`{"type":"assistant","timestamp":"2023-07-04T08:00:00Z","message":{"role":"assistant","content":"hello"}}`,
			time.Date(2023, time.July, 3, 10, 15, 0, 0, time.UTC)},
		{"no timestamp", `{"type":"user","message":{"role":"user","content":"hi"}}`, time.Time{}},
		{"unparseable timestamp", `{"type":"user","timestamp":"yesterday","message":{"role":"user","content":"hi"}}`, time.Time{}},
		{"empty span", "", time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := spanTime(tc.span); !got.Equal(tc.want) {
				t.Errorf("spanTime = %v, want %v", got, tc.want)
			}
		})
	}
}

// End to end through process_text, the path the capture hook writes on: a
// relative date in a captured session must be resolved against when it was
// said, so "yesterday" from two different sessions stays two different
// beliefs and recall can answer "when" from the claim alone.
func TestE2E_SessionCaptureAnchorsRelativeDatesToWhenTheyWereSaid(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "capture.db"))

	in := mcpProcessTextInput{
		Text:      "We moved every release onto kiln yesterday.",
		SessionID: "session-dated",
		At:        time.Date(2023, time.July, 3, 10, 15, 0, 0, time.UTC),
	}
	if _, err := mcpRunProcessText(ctx, captureActor, in); err != nil {
		t.Fatal(err)
	}

	conn, err := openConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	claims, err := conn.Claims.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, c := range claims {
		if strings.Contains(c.Text, "yesterday (2 July 2023)") {
			return
		}
		texts = append(texts, c.Text)
	}
	t.Fatalf("no claim anchored to 2 July 2023; stored %q", texts)
}
