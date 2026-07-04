package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"go.klarlabs.de/mnemos/internal/ports"
)

// SearchByText is the Postgres full-text (sparse) recall leg — the lexical
// counterpart to the pgvector `<=>` dense leg. It matches the question against
// the generated `search_tsv` column (see schema.sql) via a GIN index and ranks
// with ts_rank_cd, returning the top-N hits ordered by relevance. The query
// engine fuses these hits with the dense leg by Reciprocal Rank Fusion so exact
// tokens the embedding blurs — SHAs, service names, error codes — are recalled.
//
// Query parsing uses websearch_to_tsquery, which NEVER raises a syntax error on
// arbitrary user input (it silently ignores unbalanced quotes and stray
// operators and supports "quoted phrases", -negation, and or). That robustness
// is why it's preferred here over to_tsquery/plainto_tsquery: this path runs on
// every search, so tolerating junk input beats surfacing a parse error. A query
// that reduces to an empty tsquery (all stopwords / whitespace) simply matches
// nothing and returns no rows.
func (r EventRepository) SearchByText(ctx context.Context, query string, limit int) ([]ports.TextHit, error) {
	return searchByText(ctx, r.db, qualify(r.ns, "events"), "id", query, limit)
}

// SearchByText is the claims counterpart to EventRepository.SearchByText, over
// the claims table's generated search_tsv. Same semantics.
func (r ClaimRepository) SearchByText(ctx context.Context, query string, limit int) ([]ports.TextHit, error) {
	return searchByText(ctx, r.db, qualify(r.ns, "claims"), "id", query, limit)
}

// searchByText runs the shared tsvector match+rank against one table. idCol is
// the row's primary-key column returned as the hit id.
func searchByText(ctx context.Context, db *sql.DB, table, idCol, query string, limit int) ([]ports.TextHit, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	// websearch_to_tsquery is evaluated once and reused for both the @@ filter
	// (GIN-indexed) and the rank. ts_rank_cd rewards term proximity + density,
	// a better default than ts_rank for short records.
	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		`SELECT %s, ts_rank_cd(search_tsv, websearch_to_tsquery('english', $1)) AS score
		 FROM %s
		 WHERE search_tsv @@ websearch_to_tsquery('english', $1)
		 ORDER BY score DESC
		 LIMIT $2`, idCol, table), q, limit)
	if err != nil {
		return nil, fmt.Errorf("fts search %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
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
