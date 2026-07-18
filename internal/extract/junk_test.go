package extract

import "testing"

func TestIsJunkClaim(t *testing.T) {
	junk := []string{
		// Greetings
		"Good morning",
		"good morning!",
		"Hi there",
		"Hey Felix",
		"Hello",
		"Cheers",
		"Thanks",
		"Thank you",
		// Acknowledgements
		"Done",
		"OK",
		"Yes",
		"No",
		"Sure",
		"Got it",
		"Noted",
		// Emoji-led acks
		"✅ Done",
		"👍",
		"✅",
		// Section labels (end in colon, short)
		"So you need:",
		"The event details are:",
		"Description:",
		"Time:",
		"Items:",
		// Empty / whitespace
		"",
		"   ",
		"   .  ",
	}
	for _, s := range junk {
		t.Run("junk:"+s, func(t *testing.T) {
			if !isJunkClaim(s) {
				t.Fatalf("expected isJunkClaim(%q) = true", s)
			}
		})
	}

	keep := []string{
		"Revenue grew 15% in Q3",
		"We will migrate to PostgreSQL",
		"Users might prefer dark mode",
		"The kindergarten flea market is on Saturday",
		"Felix needs coffee and tongs",
		"Time: 8:30 AM - 2:00 PM (CEST)", // colon present but content-bearing payload
		"The team decided to use PostgreSQL after evaluating three databases",
		"Good morning sets the tone for productive standups", // greeting word in content position
	}
	for _, s := range keep {
		t.Run("keep:"+s, func(t *testing.T) {
			if isJunkClaim(s) {
				t.Fatalf("expected isJunkClaim(%q) = false", s)
			}
		})
	}
}

func TestStripDecorations(t *testing.T) {
	cases := map[string]string{
		"✅ Done":       "Done",
		"👍 noted":      "noted",
		"plain text":   "plain text",
		"":             "",
		"✅":            "",
		"100% revenue": "100% revenue", // % is punct, not symbol
	}
	for in, want := range cases {
		if got := stripDecorations(in); got != want {
			t.Errorf("stripDecorations(%q) = %q, want %q", in, got, want)
		}
	}
}

// Session capture ingests assistant transcripts, which are full of narration
// describing what the agent is about to do. That is process, not knowledge: it
// asserts nothing durable, and because such sentences share common verbs they
// generate spurious contradictions against unrelated claims. A real capture
// produced 225 of these in one sitting, 184 of them marked contested.
func TestIsJunkClaimFiltersAgentNarration(t *testing.T) {
	narration := []string{
		"Let me survey the relevant code before building",
		"Let me check existing test conventions first",
		"Let me add the endpoint first",
		"Let me explore the web app structure",
		"Let's look at the API routing",
		"Now let me run the tests",
		"First, let me read the config",
		"I'll replace scenesFromMessaging with scenesFor",
		"I'll add tests for the new endpoint",
		"I will update the fixture",
	}
	for _, s := range narration {
		if !isJunkClaim(s) {
			t.Errorf("agent narration kept as a claim: %q", s)
		}
	}

	// Must not over-filter. These assert something about the world, and a
	// first-person pronoun alone is not narration — the tell is an intent verb
	// aimed at the agent's own next action.
	keep := []string{
		"We will migrate to PostgreSQL in Q3",
		"We decided to use Go for the backend",
		"Let me know if the latency regresses", // imperative to a human, still weak but not ours to drop
		"I'll be honest, the p99 latency is 400ms",
		"The team will let the contract lapse",
		"Revenue grew 15% in Q3",
		"Users let sessions expire after 30 minutes",
	}
	for _, s := range keep {
		if isJunkClaim(s) {
			t.Errorf("over-filtered a real claim: %q", s)
		}
	}
}
