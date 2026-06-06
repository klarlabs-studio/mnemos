package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"go.klarlabs.de/mnemos/internal/ports"
)

// SearchByText runs an FTS5 BM25 query against the events_fts index
// (created in v0.10) and returns the top-N hits ordered by relevance.
// An empty query short-circuits to an empty slice rather than
// triggering an FTS5 syntax error.
//
// Quoting strategy: the query is wrapped with sanitizeFTSQuery to
// strip the FTS5 control characters (", ', :, AND/OR/NOT keywords)
// that would otherwise let an arbitrary user input cause a parse
// error. We trade away advanced query syntax for robustness — the
// query path is invoked for every search, and surfacing parse
// errors instead of "no results" would be a worse default.
func (r EventRepository) SearchByText(ctx context.Context, query string, limit int) ([]ports.TextHit, error) {
	q := sanitizeFTSQuery(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT event_id, -bm25(events_fts) AS score
		 FROM events_fts
		 WHERE events_fts MATCH ?
		 ORDER BY bm25(events_fts) ASC
		 LIMIT ?`,
		q, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("fts5 search events: %w", err)
	}
	defer closeRows(rows)
	var hits []ports.TextHit
	for rows.Next() {
		var h ports.TextHit
		if err := rows.Scan(&h.ID, &h.Score); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// SearchByText is the claims counterpart to EventRepository.SearchByText.
// Same semantics, against claims_fts.
func (r ClaimRepository) SearchByText(ctx context.Context, query string, limit int) ([]ports.TextHit, error) {
	q := sanitizeFTSQuery(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT claim_id, -bm25(claims_fts) AS score
		 FROM claims_fts
		 WHERE claims_fts MATCH ?
		 ORDER BY bm25(claims_fts) ASC
		 LIMIT ?`,
		q, limit,
	)
	if err != nil {
		// Fall back gracefully: callers can decide whether an FTS
		// outage should fail the whole query or just degrade to
		// cosine-only ranking. Wrap so the cause is preserved.
		if errors.Is(err, sql.ErrConnDone) {
			return nil, err
		}
		return nil, fmt.Errorf("fts5 search claims: %w", err)
	}
	defer closeRows(rows)
	var hits []ports.TextHit
	for rows.Next() {
		var h ports.TextHit
		if err := rows.Scan(&h.ID, &h.Score); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// sanitizeFTSQuery strips FTS5 syntax characters that would cause a
// MATCH parse error on otherwise-fine user input. We treat the query
// as a bag of bare terms — implicit AND of words — which matches
// what most users expect from "type a question and get results".
//
// Removed:
//   - quotes (FTS5 phrase delimiter; mismatched quotes blow up the parser)
//   - colons (column filter syntax)
//   - asterisks (wildcard suffix; safe but rarely intended by humans)
//   - parentheses (grouping)
//   - dashes/plus signs (NOT/REQUIRE prefixes)
//   - the bare keywords AND / OR / NOT (FTS5 logical operators)
//
// What's left: alphanumerics, underscores, and the spaces between
// terms. FTS5 implicitly ANDs them, which is the sensible default.
func sanitizeFTSQuery(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	const drop = "\"':*()[]{}+\\-^~?!"
	cleaned := strings.Map(func(r rune) rune {
		if strings.ContainsRune(drop, r) {
			return ' '
		}
		return r
	}, s)
	// Drop bare logical operators by tokenising and filtering.
	tokens := strings.Fields(cleaned)
	out := tokens[:0]
	for _, t := range tokens {
		switch strings.ToUpper(t) {
		case "AND", "OR", "NOT", "NEAR":
			continue
		}
		out = append(out, t)
	}
	return strings.Join(out, " ")
}
