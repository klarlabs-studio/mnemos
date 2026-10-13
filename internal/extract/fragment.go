package extract

import (
	"regexp"
	"strings"
)

// Two kinds of capture noise that neither the junk filter nor the rule
// engine's structural filter caught, measured on a real 111k-claim brain where
// about two thirds of a random sample were noise:
//
//   - source lines the agent was reading or writing, lifted out of their code
//     block ("type CompilationMachine interface {", "ExpiresAt     time.Time",
//     "pub signing_key: String,", "// caller asks what is currently deployed");
//   - narration fragments that announce the next piece of work without
//     asserting anything ("Now the implementation", "Now step 2: tag the
//     action", "Pushing the release", "Good so what's the remaining gap").
//
// Both are judged on shape alone, so the rules are narrow: each requires a
// marker that prose does not carry (a trailing brace, an aligned or qualified
// field type, a leading comment marker; a sequencing opener with no verb, a
// gerund with a bare object). A sentence ABOUT code — "ExpiresAt is checked
// before every token refresh" — is knowledge and passes.

// codeCommentPrefixes open a line that is a source comment.
var codeCommentPrefixes = []string{"//", "/*", "*/"}

// codeLineEndings close a line that opens a block or a call, or ends a
// statement. Prose does not end on any of them; a lead-in ending in ':' is the
// junk filter's business, not this one's. A closing '}' is deliberately absent:
// "GET /v1/beliefs/{id}" is a fact that ends in one.
const codeLineEndings = "{[(;"

var (
	// closersOnlyRE: a line that only closes blocks and calls, "}", "});".
	closersOnlyRE = regexp.MustCompile(`^[)\]};,]+$`)
	// alignedFieldRE: a struct field padded into a column, "ExpiresAt     time.Time".
	alignedFieldRE = regexp.MustCompile(`^[A-Za-z_]\w*\s{2,}[*\[\]\w.]+$`)
	// qualifiedFieldRE: a field whose type is package-qualified, "ExpiresAt time.Time".
	qualifiedFieldRE = regexp.MustCompile(`^[A-Za-z_]\w*\s+(?:\*|\[\])*[a-z]\w*\.[A-Z]\w*$`)
	// trailingCommaFieldRE: a field or literal entry ending in a comma,
	// "pub signing_key: String,".
	trailingCommaFieldRE = regexp.MustCompile(`^(?:pub(?:\([a-z]+\))?\s+)?[A-Za-z_]\w*\s*:\s*[^\s,]+,$`)
	// structTagRE: a Go struct tag, "Name string `json:\"name\"`".
	structTagRE = regexp.MustCompile("`\\w+:\"")
	// returnCallRE: a return statement that calls something.
	returnCallRE = regexp.MustCompile(`^return\s.*\w\(`)
)

// isCodeLine reports whether text is a line of source code rather than a
// statement about the world.
func isCodeLine(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	for _, p := range codeCommentPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	if strings.ContainsRune(codeLineEndings, rune(t[len(t)-1])) || closersOnlyRE.MatchString(t) {
		return true
	}
	if strings.Contains(t, ":=") || structTagRE.MatchString(t) || returnCallRE.MatchString(t) {
		return true
	}
	return alignedFieldRE.MatchString(t) || qualifiedFieldRE.MatchString(t) ||
		trailingCommaFieldRE.MatchString(t)
}

var (
	// stepMarkerRE: "Now step 2: tag the action", "Next step 3", "Phase 2".
	stepMarkerRE = regexp.MustCompile(`(?i)^(?:(?:now|next|then|first|finally)[,\s]+)?(?:step|phase|part|stage)\s+\d+\b`)
	// sequencedPhraseRE: a sequencing opener followed by a bare noun phrase,
	// "Now the implementation", "Next the doctor output".
	sequencedPhraseRE = regexp.MustCompile(`(?i)^(?:now|next|then|finally|first|ok(?:ay)?|alright|so)[,:]?\s+` +
		`(?:the|a|an|for|on|onto|to|with|our|my|this|its)\b`)
	// fillerOpenerRE: a conversational acknowledgement leading into the next
	// turn, "Good so what's the remaining gap", "Ok so now the parser".
	fillerOpenerRE = regexp.MustCompile(`(?i)^(?:good|great|ok(?:ay)?|alright|right|cool|perfect|nice|hmm|so)[,.!]?\s+` +
		`(?:so|now|then|what|let'?s)\b`)
	// openQuestionRE: a question put to the conversation, "What's the remaining gap".
	openQuestionRE = regexp.MustCompile(`(?i)^(?:what|why|how|where|when|which|who)(?:'s|’s|\s+(?:is|are|was|were|do|does|did|about|else)\b)`)
	// finiteVerbRE: a verb that turns a short phrase into an assertion, so
	// "Now the build works" is left for the other filters to judge.
	finiteVerbRE = regexp.MustCompile(`(?i)\b(?:is|are|was|were|has|have|had|will|can|must|should|does|do|did|` +
		`needs?|uses?|runs?|returns?|works?|fails?|exists?|holds?|owns?|stores?|requires?)\b`)
)

// gerundObjects are the words that follow a gerund used as an action ("Pushing
// the release") rather than as a subject ("Caching improves latency").
var gerundObjects = map[string]bool{
	"the": true, "a": true, "an": true, "this": true, "that": true, "it": true,
	"them": true, "those": true, "these": true, "our": true, "my": true,
	"up": true, "out": true, "back": true, "down": true, "over": true, "all": true,
}

// maxFragmentWords bounds the phrases treated as fragments. Past it a sentence
// carries enough content to be judged on what it says, not on how it opens.
const maxFragmentWords = 5

// isNarrationFragment reports whether text announces or asks about the work
// in progress without asserting anything that stays true.
func isNarrationFragment(text string) bool {
	t := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(text), ".!?"))
	if t == "" {
		return false
	}
	if stepMarkerRE.MatchString(t) || fillerOpenerRE.MatchString(t) || openQuestionRE.MatchString(t) {
		return true
	}
	words := strings.Fields(t)
	if len(words) > maxFragmentWords || finiteVerbRE.MatchString(t) {
		return false
	}
	return sequencedPhraseRE.MatchString(t) || isGerundAction(words)
}

// maxGerundWords is tighter than maxFragmentWords: a gerund phrase with a
// qualified object ("updating the existing record") may describe the world,
// and only the bare progress line is certain noise.
const maxGerundWords = 3

// isGerundAction reports whether words open with a gerund taking a bare
// object — the shape of a progress line, "Running the tests".
func isGerundAction(words []string) bool {
	if len(words) < 2 || len(words) > maxGerundWords {
		return false
	}
	first := strings.ToLower(words[0])
	return len(first) > 4 && strings.HasSuffix(first, "ing") && gerundObjects[strings.ToLower(words[1])]
}

// isNoiseFragment is the shape filter both extractors share: a code line or a
// narration fragment is never a claim, whichever engine produced it.
func isNoiseFragment(text string) bool {
	return isCodeLine(text) || isNarrationFragment(text)
}
