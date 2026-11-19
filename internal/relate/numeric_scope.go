package relate

import (
	"regexp"
	"strings"
	"unicode"
)

// Scope-aware numeric contradictions (relate/v6, ADR 0044).
//
// The numeric detector compares the numbers of two claims that share most of
// their words. It could not tell WHICH thing each number was about, so two
// counts of the same noun in different scopes read as one quantity: "the main
// branch has 12 failing tests" contradicted "the release branch has 3 failing
// tests". ADR 0042 measured it: 11 of the deterministic reconciliation arm's
// 13 false positives on its test split were this one shape.
//
// A scope is a qualifier that tells otherwise identical subjects apart. Two
// numeric claims are about different scopes when any of these holds:
//
//  1. A qualifier is swapped in place. Set the numbers, number words, known
//     units and restatement filler ("now", "raised", "currently") aside and
//     align what is left of the two claims on their shared words (a longest
//     common subsequence). A slot — between two shared words, before the
//     first or after the last — that holds different words on BOTH sides is a
//     swap of one qualifier for another: main/release before "branch", api/web
//     before "repo", desktop/mobile after "loads". A supersession changes the
//     number and at most inserts words ("the limit was raised to 200", "the
//     audit is now closed — 13 commits on the branch"), so it has no such slot
//     and keeps its edge. Words that differ in DIFFERENT slots are insertions
//     on each side, not a swap: two snapshots of one quantity in session
//     narration differ that way.
//  2. One side names an environment (prod, staging, dev, …) the other does
//     not — the frame detector's rule, reused.
//  3. Both sides carry a label number after the same label word ("Sprint 14",
//     "Release 3", "PR #12") with different values, next to the quantity
//     they assert. A label is an identifier, not a measurement: it names
//     which sprint, not how much.
//
// The rule only removes numeric edges, never adds one, so it errs toward
// silence — the direction this detector is meant to fail in.

// numericScopeFiller are words a restatement of one value uses to say the
// value changed, or when it was observed, without naming a different thing.
// They never count as a scope qualifier. "today"/"yesterday" are deliberately
// absent: "today's session" and "yesterday's session" are two sessions.
var numericScopeFiller = conceptSetOf(
	"now", "currently", "current", "still", "already", "anymore", "longer",
	"new", "newly", "latest", "recent", "recently", "previously", "formerly",
	"originally", "initially", "earlier", "later", "again", "back", "instead",
	"actually", "only", "just", "about", "around", "approximately", "roughly",
	"nearly", "almost", "exactly", "total", "all", "both", "also", "then",
	"raised", "lowered", "increased", "decreased", "reduced", "bumped",
	"changed", "updated", "set", "moved", "grew", "grown", "dropped", "went",
	"gone", "become", "became", "got", "gotten", "than", "there", "here",
)

// numericEnvironments is the frame detector's environment vocabulary without
// "test"/"testing": tests are the noun most often COUNTED in a numeric claim
// ("12 failing tests"), so reading the word as an environment would scope
// away the very quantity being compared.
var numericEnvironments = func() map[string]struct{} {
	out := make(map[string]struct{}, len(scopeConcepts))
	for w := range scopeConcepts {
		out[w] = struct{}{}
	}
	delete(out, conceptOf("test"))
	delete(out, conceptOf("testing"))
	return out
}()

// scopeLabelWords introduce an identifier number: "Sprint 14", "Release 3",
// "PR #12", "Phase 2". Matched lowercase and exactly.
var scopeLabelWords = map[string]struct{}{
	"sprint": {}, "version": {}, "release": {}, "session": {}, "day": {},
	"week": {}, "month": {}, "quarter": {}, "phase": {}, "step": {},
	"stage": {}, "milestone": {}, "iteration": {}, "round": {}, "build": {},
	"batch": {}, "wave": {}, "epic": {}, "ticket": {}, "issue": {}, "pr": {},
	"adr": {}, "rfc": {}, "task": {}, "chapter": {}, "section": {}, "part": {},
	"attempt": {}, "cycle": {},
}

// scopeLabelRE matches a word followed by a (possibly #-prefixed) integer.
var scopeLabelRE = regexp.MustCompile(`([A-Za-z]+)\s+#?(\d+)\b`)

// yearRE matches a calendar year, which scopes a quantity in time the way a
// label does ("in 2025 the team shipped 40 releases").
var yearRE = regexp.MustCompile(`\b(?:19|20)\d{2}\b`)

// numberTokenRE counts the number literals in a text, labels included.
var numberTokenRE = regexp.MustCompile(`\d+(?:[.,]\d+)*`)

func conceptSetOf(words ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(words))
	for _, w := range words {
		out[conceptOf(w)] = struct{}{}
	}
	return out
}

// numericScope is the part of a claim that says WHAT its numbers are about.
type numericScope struct {
	qualifiers []string          // content words in order; numbers, units, filler removed
	labels     map[string]string // label word → identifier number
}

func newNumericScope(text string) numericScope {
	return numericScope{qualifiers: scopeQualifiers(text), labels: scopeLabels(text)}
}

// scopeQualifiers returns, in sentence order, the concept tokens of text that
// can name a scope. An all-capitals word is kept even when its lowercase form
// is a stop word: "US" is a region, not "us". A token without a letter ("→",
// "—") or with a digit (a number, "180ms", "v1.2") is not a qualifier: the
// numbers are what is being compared.
func scopeQualifiers(text string) []string {
	var out []string
	for _, raw := range strings.Fields(text) {
		w := strings.ReplaceAll(strings.Trim(raw, ",.;:!?()[]{}\"'`*"), "’", "'")
		// Before the stop-word check, so "it's" is the stop word "it".
		w = strings.TrimSuffix(strings.TrimSuffix(w, "'s"), "'S")
		if w == "" || strings.ContainsAny(w, "0123456789") || !strings.ContainsFunc(w, unicode.IsLetter) {
			continue
		}
		lower := strings.ToLower(w)
		if _, _, isNumber := numberWordValue(lower); isNumber {
			continue
		}
		if _, isUnit := unitFamily[lower]; isUnit {
			continue
		}
		_, stop := stopWords[lower]
		_, neg := strictNegators[lower]
		if (stop || neg) && !isAcronym(w) {
			continue
		}
		c := conceptOf(lower)
		if _, filler := numericScopeFiller[c]; filler {
			continue
		}
		out = append(out, c)
	}
	return out
}

// qualifierSwapped reports whether two qualifier sequences, aligned on their
// longest common subsequence, hold different words in the same slot: between
// the same two shared words, before the first or after the last.
//
// Only a word the OTHER claim does not contain at all counts toward a slot. A
// word both claims use, in a different order ("74 GB of disk is free" / "92
// GB free on the disk"), falls outside the alignment without being a swap.
func qualifierSwapped(a, b []string) bool {
	inA, inB := make(map[string]struct{}, len(a)), make(map[string]struct{}, len(b))
	for _, w := range a {
		inA[w] = struct{}{}
	}
	for _, w := range b {
		inB[w] = struct{}{}
	}
	// lcs[i][j] is the length of the LCS of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	// Walk one optimal alignment, counting each side's unmatched words in the
	// current slot; a shared word closes the slot.
	i, j, gapA, gapB := 0, 0, 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j] && lcs[i][j] == lcs[i+1][j+1]+1:
			if gapA > 0 && gapB > 0 {
				return true
			}
			gapA, gapB = 0, 0
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			if _, shared := inB[a[i]]; !shared {
				gapA++
			}
			i++
		default:
			if _, shared := inA[b[j]]; !shared {
				gapB++
			}
			j++
		}
	}
	for ; i < len(a); i++ {
		if _, shared := inB[a[i]]; !shared {
			gapA++
		}
	}
	for ; j < len(b); j++ {
		if _, shared := inA[b[j]]; !shared {
			gapB++
		}
	}
	return gapA > 0 && gapB > 0
}

func isAcronym(w string) bool {
	if len(w) < 2 {
		return false
	}
	for _, r := range w {
		if !unicode.IsUpper(r) {
			return false
		}
	}
	return true
}

// scopeLabels returns the identifier numbers of text keyed by their label
// word, plus a calendar year under "year". They are labels only when the claim
// asserts some other number beside them; "The deploy uses version 16" has one
// number, and that number IS the value being asserted.
func scopeLabels(text string) map[string]string {
	labels := map[string]string{}
	for _, m := range scopeLabelRE.FindAllStringSubmatch(text, -1) {
		if _, ok := scopeLabelWords[strings.ToLower(m[1])]; ok {
			labels[strings.ToLower(m[1])] = m[2]
		}
	}
	if y := yearRE.FindString(text); y != "" {
		labels["year"] = y
	}
	if len(labels) == 0 {
		return nil
	}
	numbers := len(numberTokenRE.FindAllString(text, -1)) + len(spelledNumerics(text))
	if numbers <= len(labels) {
		return nil
	}
	return labels
}

// numericScopesDiffer reports whether two numeric claims are about different
// scopes, so a difference in their numbers is not a disagreement.
func numericScopesDiffer(a, b *contradictionView) bool {
	if scopesDiffer(a.sequence, b.sequence, numericEnvironments) {
		return true
	}
	sa, sb := a.numericScope(), b.numericScope()
	// An alignment that ties can be walked two ways, and which one a single
	// walk takes depends on argument order. Trying both keeps the verdict
	// symmetric, so the batch and incremental passes (which see a pair in
	// opposite orders) agree.
	if qualifierSwapped(sa.qualifiers, sb.qualifiers) || qualifierSwapped(sb.qualifiers, sa.qualifiers) {
		return true
	}
	for word, va := range sa.labels {
		if vb, ok := sb.labels[word]; ok && va != vb {
			return true
		}
	}
	return false
}
