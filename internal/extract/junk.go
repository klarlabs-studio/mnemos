package extract

import (
	"regexp"
	"strings"
	"unicode"
)

// isJunkClaim returns true for claim text that has no standalone value
// as a fact. Catches the common LLM-extraction pollution from chat
// transcripts (#23): greetings, list-headers ending in a colon, status
// emojis, and one-word acknowledgements that the model promotes to
// "Fact" claims.
//
// The bar is intentionally low: we only reject text that no reasonable
// reader would call a fact in isolation. Borderline cases (short but
// content-bearing claims) pass through.
func isJunkClaim(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return true
	}

	stripped := stripDecorations(t)
	if stripped == "" {
		// All content was emoji / punctuation / whitespace.
		return true
	}

	if greetingRE.MatchString(stripped) {
		return true
	}
	if ackRE.MatchString(stripped) {
		return true
	}
	if emojiAckRE.MatchString(t) {
		return true
	}
	if isSectionLabel(stripped) {
		return true
	}
	if narrationRE.MatchString(stripped) {
		return true
	}
	if metaClaimRE.MatchString(stripped) {
		return true
	}
	if contentWordCount(stripped) < 2 {
		return true
	}
	return false
}

// greetingRE matches pure greetings/sign-offs even when followed by a
// name or punctuation. Anchored so partial matches inside longer text
// don't trigger ("good morning meeting starts at 9" should pass).
var greetingRE = regexp.MustCompile(`(?i)^(good\s+(morning|afternoon|evening|night)|hi|hey|hello|cheers|thanks|thank\s+you|bye|goodbye|farewell|greetings)([\s,.!?-]+\w+)?[\s.!?]*$`)

// ackRE matches single-word acknowledgements with optional punctuation.
var ackRE = regexp.MustCompile(`(?i)^(done|ok|okay|yes|no|sure|noted|confirmed|got\s+it|copy(\s+that)?|roger|agreed)[\s.!?]*$`)

// emojiAckRE matches text that begins with a status emoji and contains
// only an emoji + optional acknowledgement word.
var emojiAckRE = regexp.MustCompile(`^[\p{So}\p{Sm}\p{Sk}]+\s*(done|ok|okay|yes|noted|confirmed|got\s+it)?[\s.!?]*$`)

// isSectionLabel catches list-headers and section titles that end with
// a colon and have no verb-like content. "So you need:" and
// "The event details are:" both match. "We decided to use Postgres:" does
// not (longer than the threshold and contains content).
func isSectionLabel(text string) bool {
	if !strings.HasSuffix(text, ":") {
		return false
	}
	body := strings.TrimRight(text, ":")
	body = strings.TrimSpace(body)
	if body == "" {
		return true
	}
	// A section label is short and has no factual payload — a bare
	// colon-suffixed phrase the model lifted from formatted text.
	return contentWordCount(body) <= 4
}

// stripDecorations removes leading/trailing emoji and punctuation so
// that "✅ Done" and "Done." normalize to the same shape before the
// greeting/ack regexes run.
func stripDecorations(text string) string {
	out := strings.Builder{}
	for _, r := range text {
		if unicode.Is(unicode.So, r) || unicode.Is(unicode.Sm, r) || unicode.Is(unicode.Sk, r) {
			continue
		}
		out.WriteRune(r)
	}
	return strings.TrimSpace(out.String())
}

// narrationRE matches assistant narration: a sentence whose subject is the
// agent and whose verb announces its own next action. Session capture ingests
// transcripts full of these ("Let me check the config", "I'll add the
// endpoint"), and they are process rather than knowledge — they assert nothing
// that stays true, and their shared vocabulary makes the contradiction
// detector pair them with unrelated claims.
//
// Deliberately narrow. It requires the agent as subject AND an intent verb, so
// "We decided to use Go" and "Users let sessions expire" are untouched; only
// the agent talking about what it is about to do is dropped.
var narrationRE = regexp.MustCompile(`(?i)^(?:(?:ok(?:ay)?|now|next|first|then|finally|so)[,\s]+)*` +
	`(?:let\s+me|let'?s|i'?ll|i\s+will|i'?m\s+going\s+to)\s+` +
	`(?:also\s+|just\s+|now\s+|quickly\s+|first\s+)*` +
	`(?:add|build|check|create|do|explore|fix|go|handle|implement|inspect|look|make|open|read|` +
	`replace|run|see|start|survey|take|test|try|update|use|verify|view|wire|write|examine|` +
	`confirm|continue|dig|drop|find|finish|get|give|keep|land|list|move|note|pick|put|remove|` +
	`rename|report|review|scan|search|set|show|split|switch|trace|walk)\b`)

// metaClaimRE matches commentary ABOUT the knowledge graph rather than about
// the world: "that's the fourth stale belief this session", "correcting the
// memory that said briefkasten was HTTP-only", "this claim is out of date".
//
// Correcting a belief in conversation used to add a SECOND claim discussing
// the first, at comparable confidence and indistinguishable in shape. The
// correction never retired anything; it buried the original under
// meta-commentary that then recalled as though it were evidence.
//
// The discriminator is the SUBJECT, not the opener. Matching on "actually" or
// "correcting" would drop the most valuable claims in the brain — real
// corrections. "Actually, the retry budget is 3 attempts, not 5" is a fact
// about the world and must survive; "actually that belief is stale" is a fact
// about the graph and must not. So this requires a memory-system noun
// (claim/belief/memory/fact recorded/…) as the thing being talked about.
//
// The memory-noun list is deliberately narrow. "entry" and "record" were
// dropped after measuring against a real brain: they matched "your shared/go.sum
// … ← stale/wrong entry", which is a fact about a lockfile, not about the graph.
//
// The memory-noun list is deliberately narrow. "entry", "record" and "fact"
// were dropped after measuring against a real brain: they matched "your
// shared/go.sum … ← stale/wrong entry", which is a fact about a lockfile, not
// about the graph.
//
// Retiring a claim is what `forget`, `update` and `memory_deprecate` are for;
// saying so in prose should not become a claim of its own.
var metaClaimRE = regexp.MustCompile(`(?i)` +
	// "<optional lead-in> that/this <memory-noun> is stale/wrong/outdated"
	`(?:^|\b)(?:that'?s|thats|this\s+is|that\s+is|it'?s)?\s*` +
	`(?:the\s+|a\s+|another\s+|\w+(?:st|nd|rd|th)\s+)?` +
	`(?:stale|outdated|obsolete|superseded|deprecated|incorrect|wrong)\s+` +
	`(?:belief|claim|memory|memories)\b` +
	// or: "<verb> the belief/claim/memory that ..."
	`|(?i)\b(?:correct(?:ing|ed)?|deprecat(?:e|ing|ed)|supersed(?:e|ing|ed)|retir(?:e|ing|ed)|forget(?:ting)?|updat(?:e|ing|ed))\s+` +
	`(?:the\s+|that\s+|this\s+|a\s+)?` +
	`(?:stale\s+|old\s+|previous\s+|existing\s+)?` +
	`(?:belief|claim|memory|memories)\b` +
	// or: "<memory-noun> that said/claimed ..."
	`|(?i)\b(?:belief|claim|memory)\s+that\s+(?:said|claimed|stated|asserted)\b`)
