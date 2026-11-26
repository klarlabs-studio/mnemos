package domain

import "strings"

// EvidenceRepeatWeight is how much a REPEATED piece of evidence from an
// already-seen source counts toward corroboration, relative to a fresh
// independent source (which counts 1.0).
//
// It is 0 (#443; it was 0.5 through trust/v3). A repeat from an origin already
// counted adds provenance, its episode stored and listed like any other, and
// leaves credibility unchanged. Repetition makes a statement feel true whether
// or not it is (the illusory-truth effect: Hasher, Goldstein & Toppino 1977;
// Fazio et al. 2015), and Mnemos's main writers are capture hooks that
// re-capture the same narration and the same assumptions across sessions, the
// setting in which repetition and truth come apart. On 141 human-labelled
// claims the change was non-inferior in calibration, not shown better
// (issue #441); consequences, through outcome credit, are what raise
// credibility.
const EvidenceRepeatWeight = 0.0

// EffectiveEvidenceCount grades a claim's evidence for corroboration by
// INDEPENDENCE, not raw volume: each distinct source (an evidence episode's
// origin, see [EvidenceOrigin]) counts fully, and same-source repeats count at
// [EvidenceRepeatWeight], which is 0. So five episodes from one document
// corroborate exactly as much as one, and no single source can manufacture
// consensus. The result is never below the distinct-source count. Storage backends pass it as the evidenceCount to
// the trust scorer; the trust formula is unchanged.
//
// It lives in domain (not trust) so every storage backend can call it without
// depending on the scoring package.
func EffectiveEvidenceCount(distinctSources, totalEvents int) int {
	if distinctSources < 0 {
		distinctSources = 0
	}
	if totalEvents < distinctSources {
		totalEvents = distinctSources
	}
	repeats := totalEvents - distinctSources
	return distinctSources + int(EvidenceRepeatWeight*float64(repeats))
}

// Event metadata keys [EvidenceOrigin] reads. They are the keys ingestion
// writes: file ingestion records the file under `input_source_path` (the
// parser prefixes input keys with `input_`), capture and Remember record the
// agent session under `session_id` (pipeline.SessionMetadataKey), and episodes
// stored before that promotion carry it under `input_session_id`.
const (
	OriginPathKey          = "input_source_path"
	OriginSessionKey       = "session_id"
	OriginLegacySessionKey = "input_session_id"
)

// EvidenceOrigin is the independence key of an evidence episode (ADR 0046):
// two evidence episodes of a belief count as one distinct source exactly when
// their origins are equal. In order of precedence:
//
//  1. "path:" + metadata[input_source_path], when non-empty. One file is one
//     source, however many chunks it was split into and whoever ingested it.
//  2. "session:" + metadata[session_id] (else metadata[input_session_id]),
//     when non-empty. One conversation is one source, however many messages
//     restate a belief.
//  3. "content:" + the episode's text with leading and trailing U+0020 spaces
//     removed and ASCII letters lower-cased. The same text re-ingested from
//     many inputs is one source.
//
// It is deliberately NOT the ingesting principal (created_by: every document
// of a single-user brain shares one, and one document ingested by two users
// is still one document) and NOT source_input_id (capture re-ingests the same
// transcript text under fresh input ids, so keying on it multiplies one
// statement into many sources). Nothing is matched by similarity (ADR 0038).
//
// This function is the definition. The in-process backends (memory, the
// recorded-as-of reconstruction) call it; the SQL backends restate it as a
// COALESCE over the same keys and must agree with it, which
// internal/store/trust_origin_parity_test.go checks on every backend.
// The normalisation is the one SQLite's trim()/lower() perform (U+0020 only,
// ASCII only), so SQLite, libSQL and Postgres (which folds with translate())
// agree byte for byte. MySQL's LOWER() also folds non-ASCII letters, so on
// MySQL two texts differing only in the case of a non-ASCII letter share an
// origin: it can count fewer sources, never more.
func EvidenceOrigin(ev Event) string {
	if p := ev.Metadata[OriginPathKey]; p != "" {
		return "path:" + p
	}
	if s := ev.Metadata[OriginSessionKey]; s != "" {
		return "session:" + s
	}
	if s := ev.Metadata[OriginLegacySessionKey]; s != "" {
		return "session:" + s
	}
	return "content:" + asciiLower(strings.Trim(ev.Content, " "))
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
