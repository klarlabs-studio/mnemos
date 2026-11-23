package extract

import (
	"regexp"
	"strings"
	"unicode"
)

// Speaker attribution (#445 item 4).
//
// A conversation is stored as "Name: utterance" lines: LoCoMo renders its
// two-person sessions that way, LongMemEval and Claude Code capture render
// "user:" / "assistant:". Extracted sentence by sentence, "Caroline: Sitting by
// the window gives me peace" became the claim "Sitting by the window gives me
// peace", detached from who said it. In a two-person conversation that loses
// the fact, and in a capture it is worse: what the assistant said about itself
// reads as a statement about the user.
//
// So when a line names its speaker, its first-person pronouns are resolved to
// that speaker and, in a two-party conversation, its second-person pronouns to
// the other party. Nothing is invented: unlabelled text keeps its own subject,
// "you" stays "you" when the addressee is not known, and "we" is never
// resolved, because who else it includes is not on the line.

// speakerLabelRE matches a line that opens with a short label and a colon.
// Whether the label names a speaker is decided by parseDialogue, not here.
var speakerLabelRE = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z'.\-]*(?: [A-Za-z][A-Za-z'.\-]*){0,2}):\s+(\S.*)$`)

// roleSpeakers are the chat-role labels transcripts use. They name a speaker
// on their own, without the repetition a personal name needs.
var roleSpeakers = map[string]string{
	"user":      "user",
	"assistant": "assistant",
}

// nonSpeakerLabels are labels that head a metadata line, not an utterance.
// A label is compared by its first word, lower-cased.
var nonSpeakerLabels = toSet(
	"note", "notes", "status", "decision", "decisions", "summary", "update",
	"question", "answer", "context", "goal", "goals", "problem", "solution",
	"description", "date", "time", "author", "owner", "reason", "result",
	"results", "example", "warning", "important", "tip", "fix", "bug", "action",
	"outcome", "feature", "edit", "reply", "response", "subject", "title",
	"todo", "done", "next", "step", "steps", "source", "sources", "reference",
	"see", "error", "info", "debug", "output", "input", "file", "path", "type",
	"name", "id", "version", "priority", "severity", "risk", "impact",
	"assumption", "hypothesis", "observation", "finding", "findings",
	"conclusion", "recommendation", "background", "overview", "details",
	"task", "tasks", "issue", "issues", "pr", "commit", "branch", "location",
	"where", "when", "why", "how", "what", "who", "caveat", "nb", "ps", "fyi",
	"re", "cc", "to", "from", "session", "day", "week", "agenda", "attendees",
	"participants", "topic", "meeting", "minutes", "objective", "requirement",
	"requirements", "scope", "deliverable", "deliverables", "milestone",
	"deadline", "rationale", "alternatives", "consequences", "pros", "cons",
	"tl", "tldr", "faq", "warn", "caution", "danger", "hint", "remember",
	"q", "a",
)

// dialogueLine is one line of a conversation with the speaker it belongs to.
// speaker is empty for lines before the first labelled one.
type dialogueLine struct {
	speaker string
	text    string
}

// parseDialogue reports whether content is a conversation with labelled
// speakers and, if so, splits it into lines with their speaker and returns the
// distinct speakers in order of appearance.
//
// A role label ("user:", "assistant:") is a speaker on its own. A personal-name
// label is accepted only when at least two lines carry one, so a lone
// "Caroline: ..." or a "Status: ..." header is not mistaken for a
// conversation. An unlabelled line continues the previous speaker's message:
// capture writes a multi-line message with its role on the first line only.
func parseDialogue(content string) ([]dialogueLine, []string, bool) {
	rawLines := strings.Split(content, "\n")
	labels := make([]string, len(rawLines))
	bodies := make([]string, len(rawLines))
	nameLines, roleLines := 0, 0
	for i, line := range rawLines {
		m := speakerLabelRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		label, body := m[1], m[2]
		switch {
		case roleSpeakers[strings.ToLower(label)] != "":
			labels[i], bodies[i] = roleSpeakers[strings.ToLower(label)], body
			roleLines++
		case isPersonLabel(label):
			labels[i], bodies[i] = label, body
			nameLines++
		}
	}
	if roleLines == 0 && nameLines < 2 {
		return nil, nil, false
	}

	var (
		out      = make([]dialogueLine, 0, len(rawLines))
		speakers []string
		seen     = map[string]struct{}{}
		current  string
	)
	for i, line := range rawLines {
		if labels[i] != "" {
			current = labels[i]
			if _, ok := seen[current]; !ok {
				seen[current] = struct{}{}
				speakers = append(speakers, current)
			}
			out = append(out, dialogueLine{speaker: current, text: bodies[i]})
			continue
		}
		out = append(out, dialogueLine{speaker: current, text: line})
	}
	return out, speakers, true
}

// isPersonLabel reports whether a label is shaped like a personal name: one to
// three capitalised words, not a known metadata label.
func isPersonLabel(label string) bool {
	if len(label) < 2 {
		return false
	}
	words := strings.Fields(label)
	if nonSpeakerLabels[strings.ToLower(strings.Trim(words[0], ".'-"))] {
		return false
	}
	for _, w := range words {
		if !unicode.IsUpper(rune(w[0])) {
			return false
		}
	}
	return true
}

// addresseeFor returns the other party of a two-person conversation, or "" when
// the conversation has any other number of speakers.
func addresseeFor(speaker string, speakers []string) string {
	if len(speakers) != 2 {
		return ""
	}
	if speakers[0] == speaker {
		return speakers[1]
	}
	return speakers[0]
}

// dialogueCandidates splits a labelled conversation into claim candidates,
// attributing each to its line's speaker. Filters run on the utterance as
// spoken, before attribution, so "Hey Mel" is still a greeting.
func dialogueCandidates(lines []dialogueLine, speakers []string) []string {
	var out []string
	for _, line := range lines {
		for _, candidate := range splitCandidates(line.text) {
			if line.speaker != "" {
				candidate = attributeSpeaker(candidate, line.speaker, addresseeFor(line.speaker, speakers))
			}
			out = append(out, candidate)
		}
	}
	return out
}

// extractCandidates is the rule-based engine's candidate splitter: speaker
// aware for a labelled conversation, unchanged for everything else.
func extractCandidates(content string) []string {
	if lines, speakers, ok := parseDialogue(content); ok {
		return dialogueCandidates(lines, speakers)
	}
	return splitCandidates(content)
}

// clauseOpeners are the words after which "you" begins a clause and is
// therefore its subject.
var clauseOpeners = toSet(
	"and", "but", "so", "that", "if", "when", "because", "or", "then", "while",
	"since", "unless", "though", "although", "where", "whether", "once",
)

// questionAuxiliaries open an inverted question ("Have you tried ...") whose
// auxiliary must agree with the resolved subject.
var questionAuxiliaries = map[string]string{
	"do": "does", "have": "has", "are": "is", "were": "was", "am": "is",
	"don't": "doesn't", "haven't": "hasn't", "aren't": "isn't", "weren't": "wasn't",
}

// attributeSpeaker rewrites a sentence spoken by speaker so its first-person
// pronouns name the speaker and, when addressee is known, its second-person
// pronouns name the addressee. The verb after a resolved subject is put in the
// third person singular. A sentence with no such pronoun is returned unchanged.
func attributeSpeaker(sentence, speaker, addressee string) string {
	tokens := strings.Fields(strings.ReplaceAll(sentence, "’", "'"))
	changed := false
	for i := 0; i < len(tokens); i++ {
		lead, core, trail := splitToken(tokens[i])
		lower := strings.ToLower(core)
		prev := ""
		if i > 0 {
			_, pc, pt := splitToken(tokens[i-1])
			if pt == "" {
				prev = strings.ToLower(pc)
			} else {
				prev = ","
			}
		}

		var (
			replacement string
			subject     bool // replacement is a clause subject; agree the next verb
		)
		switch lower {
		case "i":
			replacement, subject = displayName(speaker, i == 0), prev != "and" && prev != "or" && prev != "nor"
		case "i'm":
			replacement = displayName(speaker, i == 0) + " is"
		case "i've":
			replacement = displayName(speaker, i == 0) + " has"
		case "i'll":
			replacement = displayName(speaker, i == 0) + " will"
		case "i'd":
			replacement = displayName(speaker, i == 0) + " " + wouldOrHad(tokens, i)
		case "me", "myself":
			replacement = displayName(speaker, i == 0)
		case "my", "mine":
			replacement = possessive(displayName(speaker, i == 0))
		default:
			if addressee == "" {
				continue
			}
			switch lower {
			case "you":
				replacement = displayName(addressee, i == 0)
				if i == 1 && isQuestionAux(tokens[0]) {
					tokens[0] = conjugateToken(tokens[0], questionAuxiliaries)
				} else if i == 0 || clauseOpeners[prev] || prev == "," {
					subject = true
				}
			case "you're":
				replacement = displayName(addressee, i == 0) + " is"
			case "you've":
				replacement = displayName(addressee, i == 0) + " has"
			case "you'll":
				replacement = displayName(addressee, i == 0) + " will"
			case "you'd":
				replacement = displayName(addressee, i == 0) + " " + wouldOrHad(tokens, i)
			case "your", "yours":
				replacement = possessive(displayName(addressee, i == 0))
			case "yourself":
				replacement = displayName(addressee, i == 0)
			default:
				continue
			}
		}
		tokens[i] = lead + replacement + trail
		changed = true
		if subject && trail == "" {
			agreeNextVerb(tokens, i+1)
		}
	}
	if !changed {
		return sentence
	}
	return strings.Join(tokens, " ")
}

// displayName renders a speaker for use inside a sentence: a role reads as
// "the user", capitalised at the start of the sentence; a name as itself.
func displayName(speaker string, sentenceStart bool) string {
	if _, ok := roleSpeakers[speaker]; !ok {
		return speaker
	}
	if sentenceStart {
		return "The " + speaker
	}
	return "the " + speaker
}

func possessive(name string) string { return name + "'s" }

// wouldOrHad expands "I'd" / "you'd": "had" before a past participle ("I'd
// been"), "would" otherwise ("I'd love").
func wouldOrHad(tokens []string, i int) string {
	if i+1 < len(tokens) {
		_, next, _ := splitToken(tokens[i+1])
		next = strings.ToLower(next)
		if next == "been" || next == "never" || (strings.HasSuffix(next, "ed") && next != "need") {
			return "had"
		}
	}
	return "would"
}

func isQuestionAux(token string) bool {
	_, core, trail := splitToken(token)
	_, ok := questionAuxiliaries[strings.ToLower(core)]
	return ok && trail == ""
}

// verbSkipWords are adverbs that sit between a subject and its verb.
var verbSkipWords = toSet(
	"really", "also", "just", "still", "always", "never", "often", "usually",
	"sometimes", "actually", "totally", "absolutely", "definitely", "finally",
	"recently", "even", "only", "already", "truly", "kinda", "probably",
	"rarely", "seldom", "mostly", "generally", "honestly", "personally",
	"literally", "basically", "certainly", "especially", "currently",
)

// agreeNextVerb puts the first verb at or after tokens[start] (skipping
// adverbs) into the third person singular.
func agreeNextVerb(tokens []string, start int) {
	for j := start; j < len(tokens); j++ {
		_, core, _ := splitToken(tokens[j])
		if verbSkipWords[strings.ToLower(core)] {
			continue
		}
		tokens[j] = conjugateToken(tokens[j], nil)
		return
	}
}

// irregularThirdPerson maps verbs whose third person singular is not formed by
// the -s rules.
var irregularThirdPerson = map[string]string{
	"am": "is", "are": "is", "have": "has", "do": "does", "go": "goes",
	"were": "was", "don't": "doesn't", "haven't": "hasn't", "aren't": "isn't",
	"weren't": "wasn't",
}

// invariantVerbs already agree with any subject: modals, past forms and
// negated modals.
var invariantVerbs = toSet(
	"will", "would", "can", "could", "should", "shall", "may", "might", "must",
	"was", "did", "had", "didn't", "wasn't", "can't", "cannot", "won't",
	"wouldn't", "couldn't", "shouldn't", "hadn't", "mustn't", "used",
	"went", "saw", "made", "got", "took", "said", "came", "found", "gave",
	"thought", "felt", "knew", "left", "bought", "brought", "met", "ran",
	"began", "wrote", "heard", "kept", "lost", "told", "won", "became", "built",
	"sent", "spent", "taught", "caught", "fell", "held", "paid", "sat", "stood",
	"understood", "chose", "drove", "ate", "flew", "grew", "drew", "wore",
	"rode", "slept", "swam", "sang", "spoke", "broke", "forgot", "threw", "put",
	"let", "set", "cut", "hit", "quit", "read", "hurt", "shut", "cost", "led",
	"fed", "meant", "sold", "lent", "dealt", "drank", "struck", "hung", "dug",
)

// conjugateToken returns token with its word in the third person singular,
// keeping surrounding punctuation. A token that does not look like a lower-case
// verb is returned unchanged. overrides, when non-nil, replaces the default
// irregular table.
func conjugateToken(token string, overrides map[string]string) string {
	lead, core, trail := splitToken(token)
	if core == "" {
		return token
	}
	lower := strings.ToLower(core)
	table := irregularThirdPerson
	if overrides != nil {
		table = overrides
	}
	if v, ok := table[lower]; ok {
		return lead + matchCase(core, v) + trail
	}
	if overrides != nil {
		return token
	}
	if invariantVerbs[lower] || !isLowerWord(core) || strings.HasSuffix(lower, "ed") {
		return token
	}
	return lead + thirdPersonS(core) + trail
}

func thirdPersonS(w string) string {
	switch {
	case strings.HasSuffix(w, "s"), strings.HasSuffix(w, "sh"), strings.HasSuffix(w, "ch"),
		strings.HasSuffix(w, "x"), strings.HasSuffix(w, "z"), strings.HasSuffix(w, "o"):
		return w + "es"
	case len(w) > 1 && strings.HasSuffix(w, "y") && !strings.ContainsRune("aeiou", rune(w[len(w)-2])):
		return w[:len(w)-1] + "ies"
	default:
		return w + "s"
	}
}

// matchCase gives v the capitalisation of the word it replaces.
func matchCase(original, v string) string {
	if original != "" && unicode.IsUpper(rune(original[0])) {
		return strings.ToUpper(v[:1]) + v[1:]
	}
	return v
}

func isLowerWord(w string) bool {
	for _, r := range w {
		if !unicode.IsLower(r) && r != '\'' && r != '-' {
			return false
		}
	}
	return true
}

// splitToken separates a whitespace token into leading punctuation, the word,
// and trailing punctuation. Inner apostrophes stay with the word.
func splitToken(token string) (lead, core, trail string) {
	start := strings.IndexFunc(token, isWordRune)
	if start < 0 {
		return token, "", ""
	}
	end := strings.LastIndexFunc(token, isWordRune) + 1
	return token[:start], token[start:end], token[end:]
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func toSet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
