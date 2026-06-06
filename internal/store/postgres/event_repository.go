package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
)

// EventRepository persists domain events in the configured Postgres
// schema. Mirrors the SQLite EventRepository contract: idempotent
// Append (ON CONFLICT DO NOTHING), GetByID/ListByIDs/ListAll/ListByRunID.
type EventRepository struct {
	db *sql.DB
	ns string
}

// Append inserts the event. Append is idempotent: re-appending the
// same id is a no-op (the event store is append-only by domain
// design). Validates at the storage boundary.
func (r EventRepository) Append(ctx context.Context, event domain.Event) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("invalid event: %w", err)
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("marshal event metadata: %w", err)
	}
	_, err = r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, ingested_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9)
ON CONFLICT (id) DO NOTHING`, qualify(r.ns, "events")),
		event.ID,
		event.RunID,
		event.SchemaVersion,
		event.Content,
		event.SourceInputID,
		event.Timestamp.UTC(),
		string(metadata),
		event.IngestedAt.UTC(),
		actorOr(event.CreatedBy),
	)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

// GetByID fetches a single event.
func (r EventRepository) GetByID(ctx context.Context, id string) (domain.Event, error) {
	row := r.db.QueryRowContext(ctx, fmt.Sprintf(`
SELECT id, run_id, schema_version, content, source_input_id, timestamp, metadata_json::text, ingested_at, created_by
FROM %s WHERE id = $1`, qualify(r.ns, "events")), id)
	ev, err := scanEventRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Event{}, fmt.Errorf("event %s not found", id)
	}
	return ev, err
}

// ListByIDs returns events matching the given ids, preserving
// caller-supplied order. Missing ids are silently dropped.
func (r EventRepository) ListByIDs(ctx context.Context, ids []string) ([]domain.Event, error) {
	if len(ids) == 0 {
		return []domain.Event{}, nil
	}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, run_id, schema_version, content, source_input_id, timestamp, metadata_json::text, ingested_at, created_by
FROM %s WHERE id = ANY($1)`, qualify(r.ns, "events")), pgArray(ids))
	if err != nil {
		return nil, fmt.Errorf("query events by ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byID := map[string]domain.Event{}
	for rows.Next() {
		ev, err := scanEventRows(rows)
		if err != nil {
			return nil, err
		}
		byID[ev.ID] = ev
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events rows: %w", err)
	}
	out := make([]domain.Event, 0, len(ids))
	for _, id := range ids {
		if ev, ok := byID[id]; ok {
			out = append(out, ev)
		}
	}
	return out, nil
}

// ListAll returns every event ordered by timestamp.
func (r EventRepository) ListAll(ctx context.Context) ([]domain.Event, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, run_id, schema_version, content, source_input_id, timestamp, metadata_json::text, ingested_at, created_by
FROM %s ORDER BY timestamp ASC`, qualify(r.ns, "events")))
	if err != nil {
		return nil, fmt.Errorf("list all events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectEventRows(rows)
}

// CountAll returns the total number of events stored.
func (r EventRepository) CountAll(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s`, qualify(r.ns, "events"),
	)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count events: %w", err)
	}
	return n, nil
}

// DeleteByID removes the event with the given id. Idempotent.
func (r EventRepository) DeleteByID(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE id = $1`, qualify(r.ns, "events"),
	), id); err != nil {
		return fmt.Errorf("delete event %s: %w", id, err)
	}
	return nil
}

// DeleteAll wipes the events table.
func (r EventRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s`, qualify(r.ns, "events"),
	)); err != nil {
		return fmt.Errorf("delete all events: %w", err)
	}
	return nil
}

// ListByRunID returns every event tagged with the given run id.
func (r EventRepository) ListByRunID(ctx context.Context, runID string) ([]domain.Event, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, run_id, schema_version, content, source_input_id, timestamp, metadata_json::text, ingested_at, created_by
FROM %s WHERE run_id = $1 ORDER BY timestamp ASC`, qualify(r.ns, "events")), runID)
	if err != nil {
		return nil, fmt.Errorf("list events by run id: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectEventRows(rows)
}

func scanEventRow(row *sql.Row) (domain.Event, error) {
	var ev domain.Event
	var metadataRaw string
	if err := row.Scan(
		&ev.ID, &ev.RunID, &ev.SchemaVersion, &ev.Content,
		&ev.SourceInputID, &ev.Timestamp, &metadataRaw,
		&ev.IngestedAt, &ev.CreatedBy,
	); err != nil {
		return domain.Event{}, err
	}
	if err := unmarshalMetadata(metadataRaw, &ev.Metadata); err != nil {
		return domain.Event{}, err
	}
	return ev, nil
}

func scanEventRows(rows *sql.Rows) (domain.Event, error) {
	var ev domain.Event
	var metadataRaw string
	if err := rows.Scan(
		&ev.ID, &ev.RunID, &ev.SchemaVersion, &ev.Content,
		&ev.SourceInputID, &ev.Timestamp, &metadataRaw,
		&ev.IngestedAt, &ev.CreatedBy,
	); err != nil {
		return domain.Event{}, err
	}
	if err := unmarshalMetadata(metadataRaw, &ev.Metadata); err != nil {
		return domain.Event{}, err
	}
	return ev, nil
}

func collectEventRows(rows *sql.Rows) ([]domain.Event, error) {
	out := make([]domain.Event, 0)
	for rows.Next() {
		ev, err := scanEventRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func unmarshalMetadata(raw string, dst *map[string]string) error {
	if strings.TrimSpace(raw) == "" || raw == "null" {
		*dst = map[string]string{}
		return nil
	}
	m := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return fmt.Errorf("unmarshal event metadata: %w", err)
	}
	*dst = m
	return nil
}

// pgArray adapts a Go []string to a Postgres TEXT[] parameter for
// `ANY($1)` predicates. database/sql doesn't natively understand
// Go slices, so we fall back to the canonical "{a,b,c}" array
// literal Postgres accepts on the wire.
func pgArray(xs []string) string {
	if len(xs) == 0 {
		return "{}"
	}
	parts := make([]string, len(xs))
	for i, x := range xs {
		// Quote values containing commas/braces; for our id
		// alphabet (alphanumeric + underscores + dashes) this is
		// usually unnecessary, but cheap to cover.
		parts[i] = `"` + strings.ReplaceAll(strings.ReplaceAll(x, `\`, `\\`), `"`, `\"`) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func actorOr(s string) string {
	if s == "" {
		return domain.SystemUser
	}
	return s
}
