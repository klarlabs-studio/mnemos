package postgres

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ListBySourcePath returns the events ingested from the file at path, matched
// on the `input_source_path` metadata key, oldest first (ports.EventSourceLister).
func (r EventRepository) ListBySourcePath(ctx context.Context, path string) ([]domain.Event, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, run_id, schema_version, content, source_input_id, timestamp, metadata_json::text, ingested_at, created_by
FROM %s WHERE metadata_json->>'input_source_path' = $1 ORDER BY ingested_at ASC, id ASC`, qualify(r.ns, "events")), path)
	if err != nil {
		return nil, fmt.Errorf("list events by source path: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectEventRows(rows)
}
