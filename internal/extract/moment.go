package extract

import (
	"regexp"
	"strings"
)

// Moment-in-time operational narration (#565).
//
// A hand-labelled evaluation of live claims from real brains found most of
// them to be the agent reporting what it was doing at that moment — "Pushing
// #N", "Waiting on the lock", "Lock cleared", "CI on #N is waiting for
// approval" — or harness instructions copied out of a compaction summary. They
// are true for minutes, then recall for months at ~0.8 confidence, which is
// what broke calibration in the decay experiment (#442). The rules below catch
// the shapes that recur, each gated so that durable knowledge sharing the
// vocabulary ("Rebasing onto main avoids merge commits", "The worker waits for
// the lease before writing") survives. Under-catching keeps today's behaviour;
// over-catching deletes knowledge silently, so every rule errs short.

// opsGerundRE matches a claim that opens with a gerund naming a version-control
// or CI step. These are the verbs agents narrate their own progress with.
var opsGerundRE = regexp.MustCompile(`(?i)^(?:force-pushing|pushing|merging|rebasing|retargeting|fetching|` +
	`waiting|cherry-picking|squashing|retrying|re-?running|polling)\b`)

// maxGerundReportWords bounds a gerund progress report. The narration seen in
// practice is a handful of words ("Merging #N as-is"); a longer gerund-led
// sentence is more likely a generic statement whose verb the lexicons miss.
const maxGerundReportWords = 8

// hyphenatedRE matches a hyphenated token ("as-is", "force-push").
var hyphenatedRE = regexp.MustCompile(`\S+-\S+`)

// sWordExceptions are words ending in "s" that are not third-person verbs, so
// they do not count as evidence that a gerund is the subject of a sentence.
var sWordExceptions = map[string]bool{
	"this": true, "its": true, "thus": true, "plus": true, "us": true, "as": true,
	"across": true, "always": true, "unless": true, "yes": true,
}

// isGerundReport reports whether text is a verbless progress report opening
// with an operations gerund: "Pushing #N", "Waiting on the lock", "Pushing
// and retargeting". A gerund that is the subject of a sentence ("Rebasing onto
// main avoids merge commits", "Merging requires a green gate") has a finite
// verb; the finite-verb lexicon catches the common ones and any longer word
// ending in "s" is treated as a third-person verb, which keeps the claim.
func isGerundReport(text string) bool {
	if !opsGerundRE.MatchString(text) {
		return false
	}
	words := strings.Fields(text)
	// Hyphenated tokens are dropped before the verb check: "as-is" is not "is".
	if len(words) > maxGerundReportWords || finiteVerbRE.MatchString(hyphenatedRE.ReplaceAllString(text, "")) {
		return false
	}
	for _, w := range words[1:] {
		w = strings.ToLower(strings.Trim(w, ",.;:!?()\"`"))
		if strings.ContainsAny(w, "#'’-0123456789") || sWordExceptions[w] {
			continue
		}
		if len(w) > 3 && strings.HasSuffix(w, "s") {
			return false
		}
	}
	return true
}

// sequencedGerundRE matches "Now rebasing …", "Meanwhile polling …": a
// sequencing adverb followed by a gerund is the agent announcing its current
// step, whatever the verb.
var sequencedGerundRE = regexp.MustCompile(`(?i)^(?:now|next|meanwhile|then)[,\s]+([a-z-]{3,}ing)\b`)

// gerundShapedNouns end in "ing" without being progress verbs.
var gerundShapedNouns = map[string]bool{
	"nothing": true, "something": true, "anything": true, "everything": true,
	"thing": true, "during": true, "string": true, "morning": true, "evening": true,
}

func isSequencedGerund(text string) bool {
	m := sequencedGerundRE.FindStringSubmatch(text)
	if m == nil || gerundShapedNouns[strings.ToLower(m[1])] {
		return false
	}
	return !finiteVerbRE.MatchString(text)
}

// completedStepRE matches a terse report that a step just finished: "Lock
// cleared", "Approval noted", "All merged". At most three words, so a sentence
// that goes on to say something ("the lock is cleared by the reaper after
// 30s") never matches.
var completedStepRE = regexp.MustCompile(`(?i)^(?:\S+\s+){0,2}(?:cleared|noted|merged|pushed|landed|rebased)[.!]?$`)

// momentStatusREs match status that is stale within minutes and carries no
// knowledge once it is: a test tally, a wait, a job still in flight, an offer
// to the user, an aside about the conversation itself.
var momentStatusREs = []*regexp.Regexp{
	// "All pass, including …", "All green"
	regexp.MustCompile(`(?i)^all\s+(?:pass(?:es|ed)?|green|merged|clean|done)\b`),
	// "57 billing tests green"
	regexp.MustCompile(`(?i)^\d+\s+(?:\w+\s+){0,2}(?:tests?|checks?|suites?)\s+(?:green|red|pass(?:ing|ed)?|fail(?:ing|ed)?)\b`),
	// "Everything is merged …"
	regexp.MustCompile(`(?i)^everything\s+(?:is|'s)\s+(?:now\s+)?merged\b`),
	// "Everything green: vet, lint …" — a verbless tally of checks.
	regexp.MustCompile(`(?i)^everything\s+(?:green|passing|clean)\b`),
	// "PR #77 is fully green", "The checks are passing"
	regexp.MustCompile(`(?i)\b(?:prs?|checks?)\b(?:\s+#?\d+)?\s+(?:is|are|now)\s+(?:\w+\s+)?(?:green|red|passing|failing)\b`),
	// In-flight job status ("the import is still running", "CI is waiting for
	// approval") is NOT dropped here: it is the endpoint of a real temporal
	// contradiction ("the migration completed Tuesday" vs "the migration is
	// still running", internal/relate). extract.HalfLifeFor gives it the
	// volatile half-life instead.
	//
	// Offers to the user: "Want me to draft that reply", "Want them queued
	// next", "Shall I open …"
	regexp.MustCompile(`(?i)^(?:want\s+me\s+to|want\s+(?:them|it|this|that)\s|shall\s+i|should\s+i|do\s+you\s+want\s+me\s+to|would\s+you\s+like\s+me\s+to)\b`),
	// Asides about the conversation itself.
	regexp.MustCompile(`(?i)^your\s+(?:last\s+)?message\s+(?:looks|seems|was|got|is)\s+(?:cut\s+off|truncated|incomplete)`),
	// Review verdicts with no subject: "Nothing alarming".
	regexp.MustCompile(`(?i)^nothing\s+(?:alarming|unusual|suspicious|surprising|else|new)[.!]?$`),
}

// narrationPrefixRE splits off a short label ("Now #512:") or a leading
// subordinate clause ("Before running the patch anywhere,") so the agent
// narration behind it is judged on its own.
var narrationPrefixRE = regexp.MustCompile(`(?i)^(?:(?:\S+\s+){0,2}\S+:|(?:before|after|while|once|since)\s+[^,]{1,80},)\s+`)

// isMomentNarration reports whether stripped claim text is moment-in-time
// operational narration. See the package comment block above.
func isMomentNarration(text string) bool {
	if isGerundReport(text) || isSequencedGerund(text) || completedStepRE.MatchString(text) {
		return true
	}
	if IsHarnessInstruction(text) {
		return true
	}
	for _, re := range momentStatusREs {
		if re.MatchString(text) {
			return true
		}
	}
	if loc := narrationPrefixRE.FindStringIndex(text); loc != nil && narrationRE.MatchString(text[loc[1]:]) {
		return true
	}
	return isCompletedThenNarration(text)
}

// dashSeparators split "Lock cleared — pushing #42" into its clauses.
var dashSeparatorRE = regexp.MustCompile(`\s+[—–-]\s+`)

// isCompletedThenNarration catches "<step done> — <next step>": a completed
// step report followed only by progress narration. Every clause must be
// narration, so "Lock cleared — the reaper frees it after 30s" is kept.
func isCompletedThenNarration(text string) bool {
	parts := dashSeparatorRE.Split(text, -1)
	if len(parts) < 2 || !completedStepRE.MatchString(strings.TrimSpace(parts[0])) {
		return false
	}
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if !isGerundReport(p) && !isSequencedGerund(p) && !narrationRE.MatchString(p) {
			return false
		}
	}
	return true
}
