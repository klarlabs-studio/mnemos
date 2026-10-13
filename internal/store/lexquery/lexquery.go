// Package lexquery turns a natural-language question into the bag of terms
// the full-text backends search for.
//
// Recall's lexical leg is BM25, and BM25 is a disjunctive ranker: a document
// scores on whichever query terms it contains. The backends' native query
// syntaxes AND bare words instead, so "When did Caroline go to the LGBTQ
// support group" matched nothing unless one record held every word,
// including "when" and "did". Terms supplies the content words; each backend
// ORs them in its own syntax.
package lexquery

import (
	"strings"
	"unicode"
)

// MaxTerms bounds the disjunction so a pasted paragraph cannot turn one
// recall into a scan over every posting list in the index.
const MaxTerms = 32

// Terms returns the lower-cased content words of s, in first-seen order and
// without duplicates. A term is a run of letters or digits, which is how the
// SQLite unicode61 tokenizer and the Postgres english parser split text, so
// no punctuation ever reaches a backend's query parser. Stopwords and
// single letters are dropped; if that leaves nothing, the stopwords are kept
// so a question made only of function words still searches for something.
func Terms(s string) []string {
	var content, all []string
	seen := map[string]bool{}
	for _, tok := range strings.FieldsFunc(strings.ToLower(s), isSeparator) {
		if seen[tok] || !keep(tok) {
			continue
		}
		seen[tok] = true
		all = append(all, tok)
		if !stopwords[tok] {
			content = append(content, tok)
		}
	}
	if len(content) == 0 {
		content = all
	}
	if len(content) > MaxTerms {
		content = content[:MaxTerms]
	}
	return content
}

func isSeparator(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }

// keep drops single letters ("s" from "what's"), which match nearly every
// record, while keeping single digits, which are often the answer.
func keep(tok string) bool {
	if len([]rune(tok)) > 1 {
		return true
	}
	return unicode.IsDigit([]rune(tok)[0])
}

var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
		a about above after again against all also am an and any are as at
		be because been before being below between both but by
		can could did do does doing down during each few for from further
		had has have having he her here hers herself him himself his how
		if in into is it its itself just me more most my myself
		no nor not of off on once only or other our ours ourselves out over own
		same she should so some such than that the their theirs them themselves
		then there these they this those through to too under until up us
		very was we were what when where which while who whom why will with
		would you your yours yourself yourselves`) {
		m[w] = true
	}
	return m
}()
