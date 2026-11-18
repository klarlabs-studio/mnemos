package provenance

import (
	"strings"
	"unicode"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Late citations (ADR 0038). A reference whose source is not stored yet
// resolves to nothing at write time. [DetectAll] reports it as pending under
// canonical lookup keys; when an episode carrying one of those keys
// ([EpisodeKeys]) is written later, the citing document is detected again and
// the dependency is recorded then.
//
// A key is a cheap pre-filter, never a resolution: two spellings that share a
// canonical form (vendor-advisory, vendor.advisory) share a key, and the
// re-detection goes through the normal resolver, which decides. So a key may
// match more than resolves, never less, for every spelling the resolver
// itself accepts.

// MaxPendingKeysPerEpisode bounds the pending keys one episode records. A
// named citation has at most maxPhraseTokens words, so one reference yields at
// most 78 word runs plus its whole tokens; the bound only bites on an episode
// with several unresolved citations, and keeps one episode's pending rows from
// growing with its length.
const MaxPendingKeysPerEpisode = 256

// MaxLookupKeyLen is the longest pending key recorded, in characters. Every
// backend stores the same keys; MySQL's key-prefix limit sets the size. A
// citation whose only key is longer (a URL of more than 512 characters) is
// not resolved late.
const MaxLookupKeyLen = 512

// SourcePathKey is the metadata key that holds the path or URL an episode was
// ingested from (file and URL ingestion, parser.Normalizer). A link or a path
// written in a citation resolves through it.
const SourcePathKey = "input_source_path"

// CanonicalKey is the one canonical form of a lookup key: lower-case, with the
// separators - _ / . and whitespace collapsed to single spaces, trimmed.
func CanonicalKey(s string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r == '-' || r == '_' || r == '/' || r == '.' || unicode.IsSpace(r):
			return ' '
		default:
			return unicode.ToLower(r)
		}
	}, s)
	return strings.Join(strings.Fields(mapped), " ")
}

// EpisodeKeys are the canonical keys under which a citation can name ev: its
// id, its source_input_id and the path or URL it was ingested from, in that
// order, without empties or repeats.
func EpisodeKeys(ev domain.Event) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range []string{ev.ID, ev.SourceInputID, ev.Metadata[SourcePathKey]} {
		if k := CanonicalKey(raw); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// phraseKeys are the pending keys of a named citation that resolved to
// nothing: every contiguous run of its words (as nameRuns builds them, at most
// maxPhraseTokens words) joined by single spaces, without a lone stop word,
// longest first; then the canonical form of each whole token, which differs
// from a run only when the token holds a character other than a separator
// (inline:ev_1, a source id written out with its colon).
func phraseKeys(phrase string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(k string) {
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	words := strings.FieldsFunc(strings.ToLower(phrase), func(r rune) bool { return !isWordRune(r) })
	if len(words) > maxPhraseTokens {
		words = words[:maxPhraseTokens]
	}
	for n := len(words); n >= 1; n-- {
		for i := 0; i+n <= len(words); i++ {
			run := words[i : i+n]
			if n == 1 && stopWords[run[0]] {
				continue
			}
			add(strings.Join(run, " "))
		}
	}
	for _, tok := range rawTokens(phrase) {
		add(CanonicalKey(tok))
	}
	return out
}
