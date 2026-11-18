// Package provenance detects source dependence between episodes (ADR 0038).
//
// An episode depends on another only when it says so. Three deterministic
// signals count: provenance metadata (cites, copied_from, derived_from), a URL
// in the text, and a citation marker ("Source:", "according to", ...) whose
// phrase names a stored source. Text similarity is deliberately NOT a signal:
// identical sentences are also what independent corroboration looks like, so a
// similarity rule would withdraw sound beliefs along with copied ones.
//
// [References] is pure: it finds what an episode cites. [Detect] resolves those
// references against stored episodes and returns the dependencies to record.
package provenance

import (
	"sort"
	"strings"
	"unicode"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Reference is one citation found in an episode, before resolution.
type Reference struct {
	Kind   domain.DependencyKind
	Signal domain.DependencySignal
	// Text is the citation as it appears (the line, span, URL or metadata
	// entry). It is recorded on the dependency so a reader can check it.
	Text string
	// Phrase is what a citation marker named (named_source only).
	Phrase string
	// Exact are identifiers that must equal an event id, a source_input_id or
	// an input_source_path exactly (metadata and link signals).
	Exact []string
}

// marker is a citation marker and the kind of dependency it declares.
type marker struct {
	text string
	kind domain.DependencyKind
}

// lineMarkers start a line and are followed by a colon.
var lineMarkers = []marker{
	{"quoted from", domain.DependencyCopies},
	{"copied from", domain.DependencyCopies},
	{"reposted from", domain.DependencyCopies},
	{"derived from", domain.DependencyDerivedFrom},
	{"based on", domain.DependencyDerivedFrom},
	{"sources", domain.DependencyCites},
	{"source", domain.DependencyCites},
	{"via", domain.DependencyCites},
	{"references", domain.DependencyCites},
	{"reference", domain.DependencyCites},
	{"citation", domain.DependencyCites},
	{"credit", domain.DependencyCites},
}

// inlineMarkers appear anywhere in a line; the phrase runs to the next
// punctuation mark.
var inlineMarkers = []marker{
	{"according to", domain.DependencyCites},
	{"as reported by", domain.DependencyCites},
	{"as reported in", domain.DependencyCites},
	{"quoting", domain.DependencyCopies},
	{"citing", domain.DependencyCites},
}

// metadataKeys are the explicit provenance keys, without and with the input_
// prefix file and API ingestion add (parser.Normalizer).
var metadataKeys = map[string]domain.DependencyKind{
	"cites": domain.DependencyCites, "copied_from": domain.DependencyCopies, "derived_from": domain.DependencyDerivedFrom,
	"input_cites": domain.DependencyCites, "input_copied_from": domain.DependencyCopies, "input_derived_from": domain.DependencyDerivedFrom,
}

// maxPhraseTokens bounds a named-source phrase; a citation names a source in
// a few words, and the bound keeps the candidate spellings small.
const maxPhraseTokens = 12

// References returns the citations ev makes, in a stable order: metadata
// (by key), then the text's markers and links in reading order.
func References(ev domain.Event) []Reference {
	var out []Reference
	keys := make([]string, 0, len(ev.Metadata))
	for k := range ev.Metadata {
		if _, ok := metadataKeys[k]; ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		var ids []string
		for _, v := range strings.Split(ev.Metadata[k], ",") {
			if v = strings.TrimSpace(v); v != "" {
				ids = append(ids, v)
			}
		}
		if len(ids) > 0 {
			out = append(out, Reference{Kind: metadataKeys[k], Signal: domain.SignalMetadata, Text: k + ": " + ev.Metadata[k], Exact: ids})
		}
	}
	seenURL := map[string]bool{}
	for _, line := range strings.Split(ev.Content, "\n") {
		if ref, ok := lineReference(line); ok {
			out = append(out, ref)
		} else {
			out = append(out, inlineReferences(line)...)
		}
		for _, u := range urls(line) {
			if !seenURL[u] {
				seenURL[u] = true
				out = append(out, Reference{Kind: domain.DependencyCites, Signal: domain.SignalLink, Text: u, Exact: []string{u}})
			}
		}
	}
	return out
}

// lineReference reads a line that starts with a citation marker and a colon.
// Bullets, quote marks and markdown emphasis before the marker are ignored.
func lineReference(line string) (Reference, bool) {
	text := strings.TrimSpace(line)
	body := strings.ReplaceAll(strings.TrimLeft(text, "-*+>• \t"), "*", "")
	lower := strings.ToLower(body)
	for _, m := range lineMarkers {
		if !strings.HasPrefix(lower, m.text) {
			continue
		}
		rest := strings.TrimSpace(body[len(m.text):])
		if !strings.HasPrefix(rest, ":") {
			continue
		}
		phrase := cleanPhrase(rest[1:])
		if phrase == "" {
			return Reference{}, false
		}
		return Reference{Kind: m.kind, Signal: domain.SignalNamedSource, Text: text, Phrase: phrase}, true
	}
	return Reference{}, false
}

// inlineReferences reads "according to X," style citations in a line.
func inlineReferences(line string) []Reference {
	var out []Reference
	lower := strings.ToLower(line)
	for _, m := range inlineMarkers {
		from := 0
		for {
			i := strings.Index(lower[from:], m.text)
			if i < 0 {
				break
			}
			start := from + i
			end := start + len(m.text)
			from = end
			if (start > 0 && isWordRune(rune(lower[start-1]))) || (end < len(lower) && isWordRune(rune(lower[end]))) {
				continue
			}
			stop := strings.IndexAny(line[end:], ",.;:!?()\"")
			raw := line[end:]
			if stop >= 0 {
				raw = line[end : end+stop]
			}
			phrase := cleanPhrase(raw)
			if phrase == "" {
				continue
			}
			out = append(out, Reference{
				Kind: m.kind, Signal: domain.SignalNamedSource,
				Text: strings.TrimSpace(line[start:end] + raw), Phrase: phrase,
			})
		}
	}
	return out
}

// cleanPhrase trims a marker's phrase: surrounding space and punctuation, and
// everything after maxPhraseTokens words.
func cleanPhrase(s string) string {
	fields := strings.Fields(s)
	if len(fields) > maxPhraseTokens {
		fields = fields[:maxPhraseTokens]
	}
	return strings.Trim(strings.Join(fields, " "), " .,;:!?\"'()[]<>*")
}

// urls returns the http(s) URLs in line, trimmed of surrounding punctuation.
func urls(line string) []string {
	var out []string
	for _, f := range strings.Fields(line) {
		f = strings.TrimLeft(f, "(<[\"'")
		f = strings.TrimRight(f, ").,;:!?'\">]")
		if strings.HasPrefix(f, "http://") || strings.HasPrefix(f, "https://") {
			out = append(out, f)
		}
	}
	return out
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// stopWords never match a source on their own.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "in": true, "on": true, "by": true, "to": true,
	"and": true, "or": true, "for": true, "from": true, "at": true, "our": true, "their": true,
	"its": true, "this": true, "that": true, "these": true, "those": true, "his": true, "her": true,
}

// rawTokens are a phrase's whitespace-separated words, trimmed of surrounding
// punctuation: candidates for an exact identifier (an event id, a
// source_input_id or a path written out in the citation).
func rawTokens(phrase string) []string {
	var out []string
	for _, f := range strings.Fields(phrase) {
		if f = strings.Trim(f, ".,;:!?\"'()[]<>*"); f != "" && !stopWords[strings.ToLower(f)] {
			out = append(out, f)
		}
	}
	return out
}

// nameRuns are the spellings a source_input_id could have inside phrase: every
// contiguous run of its words, lower-cased and joined by "-", "_" or a space,
// grouped by run length, longest first. A lone stop word is never a run.
func nameRuns(phrase string) [][]string {
	words := strings.FieldsFunc(strings.ToLower(phrase), func(r rune) bool { return !isWordRune(r) })
	if len(words) > maxPhraseTokens {
		words = words[:maxPhraseTokens]
	}
	var out [][]string
	for n := len(words); n >= 1; n-- {
		seen := map[string]bool{}
		var group []string
		for i := 0; i+n <= len(words); i++ {
			run := words[i : i+n]
			if n == 1 && stopWords[run[0]] {
				continue
			}
			for _, sep := range []string{"-", "_", " "} {
				v := strings.Join(run, sep)
				if !seen[v] {
					seen[v] = true
					group = append(group, v)
				}
			}
		}
		if len(group) > 0 {
			out = append(out, group)
		}
	}
	return out
}
