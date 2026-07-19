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

// Long lead-ins — full sentences ending in a colon whose payload lives
// elsewhere — are the largest narration class the census exposed, and the
// <=4-word section-label cap let them through. Every colon-ender in a spread
// sample of a real brain was a lead-in, not an assertion.
func TestIsJunkClaim_LongLeadIns(t *testing.T) {
	leadIns := []string{
		"Confirmed absent from every downstream surface:",
		"Finding #1 is fixed, with the scope somewhat larger than the original diagnosis:",
		"Now the two read paths (LoadRollout and ListRollouts):",
		"What shipped (3 atomic commits on main):",
		"Two things I've stopped on rather than guess:",
		"The documented fix is to await router.isReady() before mounting:",
	}
	for _, s := range leadIns {
		if !isJunkClaim(s) {
			t.Errorf("kept a lead-in as a claim: %q", s)
		}
	}
}

// The colon is the signal, but it must not eat real knowledge. A claim that
// asserts something and merely happens to end in a colon-free clause, or is
// structured data, must survive.
func TestIsLeadIn_KeepsKnowledgeAndData(t *testing.T) {
	// A bare trailing colon IS the junk signal — real structured knowledge
	// ("MNEMOS_CAPTURE_TIMEOUT = 4m") does not end in one. The safety property
	// that matters is that isLeadIn never fires on a claim WITHOUT a trailing
	// colon, so ordinary assertions are untouchable by this rule.
	nonColon := []string{
		"we chose Postgres because the write volume outgrew SQLite",
		"the retry budget is 3 attempts, not 5",
		"config precedence: env var overrides file", // colon mid-sentence, payload present
		"the three environments are dev, staging, and prod",
		"MNEMOS_CAPTURE_TIMEOUT = 4m by default",
	}
	for _, s := range nonColon {
		if isLeadIn(s) {
			t.Errorf("isLeadIn fired on a non-lead-in claim: %q", s)
		}
		if isJunkClaim(s) {
			t.Errorf("dropped real content: %q", s)
		}
	}
	// A colon-ending claim that carries structured payload is exempted so a
	// genuine "the mapping is: {..." style is not eaten by the prose rule.
	if isLeadIn("the resulting config is: {timeout=4m}:") {
		t.Error("isLeadIn ate a structured-payload colon claim")
	}
}

// Short colon phrases stay with isSectionLabel; the two rules must not disagree
// about the same input.
func TestIsLeadIn_DefersShortToSectionLabel(t *testing.T) {
	if isLeadIn("Getting the detail:") {
		t.Error("isLeadIn claimed a short phrase that belongs to isSectionLabel")
	}
	// But it's still junk overall, via isSectionLabel.
	if !isJunkClaim("Getting the detail:") {
		t.Error("short colon phrase not caught by either rule")
	}
}
