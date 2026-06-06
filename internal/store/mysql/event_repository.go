package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
)

// EventRepository implements ports.EventRepository against MySQL.
type EventRepository struct {
	db *sql.DB
}

// Append inserts an event idempotently. INSERT IGNORE is the MySQL
// analog of SQLite's INSERT OR IGNORE / Postgres's ON CONFLICT DO
// NOTHING — re-appending the same id is a silent no-op.
func (r EventRepository) Append(ctx context.Context, event domain.Event) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("invalid event: %w", err)
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("marshal event metadata: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
INSERT IGNORE INTO events (id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, ingested_at, created_by)
VALUES (?, ?, ?, ?, ?, ?, CAST(? AS JSON), ?, ?)`,
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

// GetByID returns the event with the given id.
func (r EventRepository) GetByID(ctx context.Context, id string) (domain.Event, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, ingested_at, created_by
FROM events WHERE id = ?`, id)
	ev, err := scanEventRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Event{}, fmt.Errorf("event %s not found", id)
	}
	return ev, err
}

// ListByIDs returns events matching the given ids in caller order.
func (r EventRepository) ListByIDs(ctx context.Context, ids []string) ([]domain.Event, error) {
	if len(ids) == 0 {
		return []domain.Event{}, nil
	}
	placeholders, args := inPlaceholders(ids)
	//nolint:gosec // G202: placeholders are literal "?" tokens, not user input
	q := `SELECT id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, ingested_at, created_by
FROM events WHERE id IN (` + placeholders + `)`
	rows, err := r.db.QueryContext(ctx, q, args...)
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
	rows, err := r.db.QueryContext(ctx, `
SELECT id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, ingested_at, created_by
FROM events ORDER BY timestamp ASC`)
	if err != nil {
		return nil, fmt.Errorf("list all events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectEventRows(rows)
}

// CountAll returns the total number of events stored.
func (r EventRepository) CountAll(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count events: %w", err)
	}
	return n, nil
}

// DeleteByID removes the event with the given id. Idempotent.
func (r EventRepository) DeleteByID(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete event %s: %w", id, err)
	}
	return nil
}

// DeleteAll wipes the events table.
func (r EventRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM events`); err != nil {
		return fmt.Errorf("delete all events: %w", err)
	}
	return nil
}

// ListByRunID returns every event tagged with the given run id.
func (r EventRepository) ListByRunID(ctx context.Context, runID string) ([]domain.Event, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, ingested_at, created_by
FROM events WHERE run_id = ? ORDER BY timestamp ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list events by run id: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectEventRows(rows)
}

func scanEventRow(row *sql.Row) (domain.Event, error) {
	var ev domain.Event
	var metadataRaw []byte
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
	var metadataRaw []byte
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

func unmarshalMetadata(raw []byte, dst *map[string]string) error {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		*dst = map[string]string{}
		return nil
	}
	m := map[string]string{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("unmarshal event metadata: %w", err)
	}
	*dst = m
	return nil
}

func actorOr(s string) string {
	if s == "" {
		return domain.SystemUser
	}
	return s
}
