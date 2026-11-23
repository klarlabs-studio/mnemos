package extract

import (
	"regexp"
	"strings"
)

// harnessInstructionRE matches instructions the Claude Code harness writes
// into the conversation around a context compaction or an interrupt.
//
// Capture strips them (cmd/mnemos/transcript_filter.go calls
// IsHarnessInstruction), but only since #469: before it, only the line
// carrying the compaction marker was removed, and the instructions that
// follow it in the same message were stored as facts. Extraction applies the
// same rule so `prune --narration` can clean those brains, and so text that
// reaches extraction by another path (process_text, /v1/process) is covered.
var harnessInstructionRE = regexp.MustCompile(`(?i)^[(*_\s]*(?:` +
	`(?:please\s+)?continue the conversation from where (?:it|we|you) left` +
	`|pick up the last task as if the break never happened` +
	`|resume directly\b.{0,10}do not (?:acknowledge|recap)` +
	`|if you need specific details from before compaction` +
	`|no new user messages (?:were sent|arrived)` +
	`|\[request interrupted by user` +
	`)`)

// IsHarnessInstruction reports whether a line is harness boilerplate rather
// than anything a person or the agent said: the compaction continuation
// instructions, the session-summary notes that accompany them, and the
// interrupt marker. It is the single definition shared by capture-time
// stripping and extraction.
func IsHarnessInstruction(line string) bool {
	return harnessInstructionRE.MatchString(strings.TrimSpace(line))
}
