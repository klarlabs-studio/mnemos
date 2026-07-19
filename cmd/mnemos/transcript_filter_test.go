package main

import (
	"strings"
	"testing"
)

// Every input here is text that actually reached a production brain as claims.
func TestStripHarnessText_RemovesInjectedContent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" means the whole message was harness output
	}{
		{
			name: "system reminder block",
			in:   "Here is the real question.\n<system-reminder>\nBase directory for this skill: /Users/x/.claude/skills/capture\nWrite `./memory/sessions/[YYYY-MM-DD].md`\n</system-reminder>",
			want: "Here is the real question.",
		},
		{
			name: "task notification",
			in:   "<task-notification>\n<task-id>abc</task-id>\n<note>A task-notification fires each time this agent stops</note>\n</task-notification>",
			want: "",
		},
		{
			name: "bare note block",
			in:   "<note>A task-notification fires each time this agent stops with no live background children</note>",
			want: "",
		},
		{
			name: "resume preamble",
			in:   "This session is being continued from a previous conversation that ran out of context.\nThe summary below covers the earlier portion of the conversation.\nResume directly — do not acknowledge the summary.",
			want: "",
		},
		{
			name: "system notification banner",
			in:   "[SYSTEM NOTIFICATION - NOT USER INPUT]\nThis is an automated background-task event.",
			want: "This is an automated background-task event.",
		},
		{
			name: "skill preamble",
			in:   "Base directory for this skill: /Users/x/.claude/skills/brief\nActually useful sentence about the deploy.",
			want: "Actually useful sentence about the deploy.",
		},
		{
			name: "hook context injection",
			in:   "UserPromptSubmit hook additional context: Relevant knowledge from Mnemos\nThe real user message.",
			want: "The real user message.",
		},
		{
			name: "command wrapper",
			in:   "<command-name>capture</command-name>\n<command-args></command-args>\nwrap up the session",
			want: "wrap up the session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripHarnessText(tt.in)
			if got != tt.want {
				t.Errorf("stripHarnessText()\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// The filter must not eat real conversation. This is the failure that would
// matter most: silently dropping the knowledge capture exists to keep.
func TestStripHarnessText_PreservesConversation(t *testing.T) {
	keep := []string{
		"We decided to use Postgres because the write volume outgrew SQLite.",
		"The deploy failed because the migration lock was still held.",
		"Note: the retry budget is 3 attempts, not 5.",
		"I reviewed the system reminder design and it looks fine.",
		"The session summary should mention the rollback.",
		"Analysis of the outage showed a DNS timeout.",
		"base directory conventions differ between the two repos",
	}
	for _, s := range keep {
		if got := stripHarnessText(s); got != s {
			t.Errorf("dropped or altered real content:\n got: %q\nwant: %q", got, s)
		}
	}
}

// A message that is only harness output must reduce to empty so the caller
// skips it entirely rather than ingesting whitespace.
func TestStripHarnessText_WhollyHarnessBecomesEmpty(t *testing.T) {
	in := "<system-reminder>anything at all</system-reminder>\n\n<note>and this</note>"
	if got := stripHarnessText(in); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// Multi-line and multi-block messages: the common real shape.
func TestStripHarnessText_MultipleBlocks(t *testing.T) {
	in := strings.Join([]string{
		"<system-reminder>skill text</system-reminder>",
		"First real line.",
		"<task-notification><note>noise</note></task-notification>",
		"Second real line.",
	}, "\n")
	got := stripHarnessText(in)
	for _, want := range []string{"First real line.", "Second real line."} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q from %q", want, got)
		}
	}
	for _, bad := range []string{"skill text", "noise", "system-reminder", "task-notification"} {
		if strings.Contains(got, bad) {
			t.Errorf("kept harness fragment %q in %q", bad, got)
		}
	}
}

func TestStripHarnessText_Empty(t *testing.T) {
	if got := stripHarnessText(""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
