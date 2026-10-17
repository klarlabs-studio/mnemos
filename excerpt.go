package mnemos

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxExcerptChars bounds one belief's source excerpt (ADR 0031). It holds a
// source line and its neighbours, while many beliefs still fit a reader's
// context budget.
const maxExcerptChars = 400

// attachExcerpts sets each result's Excerpt to the part of its evidence that
// best matches the belief. It runs after Recall's limit, so it reads the
// evidence of the returned beliefs only: one evidence lookup, one event lookup.
func (m *memory) attachExcerpts(ctx context.Context, results []Result) error {
	if len(results) == 0 {
		return nil
	}
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ClaimID
	}
	links, err := m.conn.Claims.ListEvidenceByClaimIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("mnemos: excerpts: list evidence: %w", err)
	}
	eventIDs := make([]string, 0, len(links))
	seen := make(map[string]bool, len(links))
	for _, l := range links {
		if !seen[l.EventID] {
			seen[l.EventID] = true
			eventIDs = append(eventIDs, l.EventID)
		}
	}
	events, err := m.conn.Events.ListByIDs(ctx, eventIDs)
	if err != nil {
		return fmt.Errorf("mnemos: excerpts: list events: %w", err)
	}
	content := make(map[string]string, len(events))
	for _, e := range events {
		content[e.ID] = e.Content
	}
	sources := make(map[string][]string, len(results))
	for _, l := range links {
		if c, ok := content[l.EventID]; ok {
			sources[l.ClaimID] = append(sources[l.ClaimID], c)
		}
	}
	for i := range results {
		results[i].Excerpt = bestExcerpt(results[i].Text, sources[results[i].ClaimID], maxExcerptChars)
	}
	return nil
}

// bestExcerpt returns the segment of the sources that shares the most words
// with the belief, with at most one neighbouring segment on each side while
// the whole stays within max characters. Segments are lines; a source with
// one long line is split into sentences. Empty when no segment shares a word.
//
// Extraction does not record which words a belief came from, so the source
// is found again by overlap. This is the same rule extraction uses to link a
// belief to an event.
func bestExcerpt(belief string, sources []string, max int) string {
	want := excerptTokens(belief)
	if len(want) == 0 {
		return ""
	}
	var bestSegs []string
	bestAt, bestScore, sep := -1, 0, "\n"
	for _, src := range sources {
		segs, s := excerptSegments(src, max)
		for i, seg := range segs {
			if score := overlap(want, excerptTokens(seg)); score > bestScore {
				bestSegs, bestAt, bestScore, sep = segs, i, score, s
			}
		}
	}
	if bestAt < 0 {
		return ""
	}
	out := truncateRunes(bestSegs[bestAt], max)
	if bestAt > 0 && len(out)+len(sep)+len(bestSegs[bestAt-1]) <= max {
		out = bestSegs[bestAt-1] + sep + out
	}
	if bestAt+1 < len(bestSegs) && len(out)+len(sep)+len(bestSegs[bestAt+1]) <= max {
		out = out + sep + bestSegs[bestAt+1]
	}
	return out
}

// excerptSegments splits a source into lines, or into sentences when it is a
// single line longer than max. It returns the separator to rejoin them with.
func excerptSegments(src string, max int) ([]string, string) {
	var lines []string
	for _, l := range strings.Split(src, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || len(lines[0]) <= max {
		return lines, "\n"
	}
	var sentences []string
	start := 0
	text := lines[0]
	for i := 0; i < len(text)-1; i++ {
		if (text[i] == '.' || text[i] == '!' || text[i] == '?') && text[i+1] == ' ' {
			sentences = append(sentences, strings.TrimSpace(text[start:i+1]))
			start = i + 1
		}
	}
	if rest := strings.TrimSpace(text[start:]); rest != "" {
		sentences = append(sentences, rest)
	}
	return sentences, " "
}

// excerptStopwords are words too common to say which line a belief came from.
var excerptStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true,
	"was": true, "are": true, "has": true, "have": true, "had": true, "from": true,
	"not": true, "but": true, "you": true, "she": true, "his": true, "her": true,
	"they": true, "their": true, "about": true, "also": true, "who": true, "what": true,
}

func excerptTokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if utf8.RuneCountInString(w) >= 3 && !excerptStopwords[w] {
			out[w] = true
		}
	}
	return out
}

func overlap(want, have map[string]bool) int {
	n := 0
	for w := range have {
		if want[w] {
			n++
		}
	}
	return n
}

// truncateRunes cuts s to at most max bytes without splitting a character,
// marking the cut with an ellipsis.
func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const ellipsis = "…"
	cut := max - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}
