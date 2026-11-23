package extract

import (
	"strings"
	"testing"
)

// TestPrompt_SpeakerAttribution pins the LLM half of #445 item 4. LoCoMo
// extraction stored "Sitting in that spot by the window gives me peace" with
// no speaker; the prompt must tell the model to decontextualise each claim,
// resolve first and second person to the labelled speaker and addressee, keep
// the assistant's self-statements off the user, and invent no speaker.
// PromptVersion is part of the LLM cache key, so changing the instructions
// without bumping it would keep serving extractions made under the old ones.
func TestPrompt_SpeakerAttribution(t *testing.T) {
	if PromptVersion != "v1.7" {
		t.Errorf("PromptVersion = %q, want v1.7 (bump it whenever the system prompt changes)", PromptVersion)
	}
	for _, want := range []string{
		"stand alone",
		"Name: utterance",
		"I, me, my, mine, myself",
		"you, your, yours, yourself",
		"the other speaker",
		"never about the user",
		"do NOT invent a speaker",
		"Caroline finds peace sitting in that spot by the window",
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Errorf("system prompt is missing %q", want)
		}
	}
}

// TestPrompt_MomentNarration pins the LLM half of #565: the model must not turn
// the agent's progress reports, in-progress status, step lead-ins or harness
// instructions into claims, and must not state a single run's result as a
// general fact.
func TestPrompt_MomentNarration(t *testing.T) {
	for _, want := range []string{
		"Progress narration",
		"Pushing #12",
		"In-progress status",
		"Step headers and lead-ins",
		"harness or system instructions",
		"single run",
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Errorf("system prompt is missing %q", want)
		}
	}
}
