package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store/lexquery"
)

// SearchByText runs an FTS5 BM25 query against the events_fts index
// (created in v0.10) and returns the top-N hits ordered by relevance.
// An empty query short-circuits to an empty slice rather than
// triggering an FTS5 syntax error.
//
// The query is natural language, not FTS5 syntax: ftsMatchQuery reduces
// it to quoted content words joined with OR, so no input can raise a
// parse error and a record need not contain every word to be ranked.
func (r EventRepository) SearchByText(ctx context.Context, query string, limit int) ([]ports.TextHit, error) {
	q := ftsMatchQuery(query)
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
	q := ftsMatchQuery(query)
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

// ftsMatchQuery builds the FTS5 MATCH expression for a natural-language
// query: the content words from [lexquery.Terms], each quoted as an FTS5
// string so no word can act as an operator, joined with OR.
//
// OR, not FTS5's implicit AND of bare terms: bm25() ranks a record by the
// query terms it holds, so a disjunction keeps the best record on top while
// still matching records that lack some words. Under AND a question matched
// only records containing every word of it, including "when" and "did".
// Quoting also means punctuation never reaches the parser; "2.5" arrives as
// the terms "2" and "5" instead of an FTS5 syntax error.
func ftsMatchQuery(s string) string {
	terms := lexquery.Terms(s)
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + t + `"`
	}
	return strings.Join(quoted, " OR ")
}
