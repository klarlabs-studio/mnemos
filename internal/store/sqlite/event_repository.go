package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store/sqlite/sqlcgen"
)

// EventRepository provides SQLite-backed storage for domain events.
type EventRepository struct {
	db *sql.DB
	q  *sqlcgen.Queries
}

// NewEventRepository returns an EventRepository backed by the given database.
func NewEventRepository(db *sql.DB) EventRepository {
	return EventRepository{db: db, q: sqlcgen.New(db)}
}

// Append persists a single domain event to the database. The event
// must pass domain.Event.Validate() — storage is the last line of
// defense, so malformed events that slipped past earlier checks get
// rejected here rather than polluting the store.
func (r EventRepository) Append(ctx context.Context, event domain.Event) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("invalid event: %w", err)
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("marshal event metadata: %w", err)
	}

	err = r.q.CreateEvent(ctx, sqlcgen.CreateEventParams{
		ID:            event.ID,
		RunID:         event.RunID,
		SchemaVersion: event.SchemaVersion,
		Content:       event.Content,
		SourceInputID: event.SourceInputID,
		Timestamp:     event.Timestamp.UTC().Format(time.RFC3339Nano),
		MetadataJson:  string(metadata),
		IngestedAt:    event.IngestedAt.UTC().Format(time.RFC3339Nano),
		CreatedBy:     actorOr(event.CreatedBy),
	})
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}

	return nil
}

// GetByID retrieves a single event by its unique identifier.
func (r EventRepository) GetByID(ctx context.Context, id string) (domain.Event, error) {
	row, err := r.q.GetEventByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Event{}, fmt.Errorf("event %s not found", id)
	}
	if err != nil {
		return domain.Event{}, err
	}

	event, err := mapSQLEvent(row)
	if err != nil {
		return domain.Event{}, err
	}
	return event, nil
}

// ListByIDs returns events matching the given IDs, preserving the input order.
func (r EventRepository) ListByIDs(ctx context.Context, ids []string) ([]domain.Event, error) {
	if len(ids) == 0 {
		return []domain.Event{}, nil
	}

	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}

	query := fmt.Sprintf(`
SELECT id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, ingested_at, created_by
FROM events
WHERE id IN (%s)`, strings.Join(placeholders, ",")) //nolint:gosec // G201: placeholders are literal "?" strings, not user input

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query events by ids: %w", err)
	}
	defer closeRows(rows)

	byID := map[string]domain.Event{}
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		byID[event.ID] = event
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events rows: %w", err)
	}

	ordered := make([]domain.Event, 0, len(ids))
	for _, id := range ids {
		event, ok := byID[id]
		if !ok {
			continue
		}
		ordered = append(ordered, event)
	}

	return ordered, nil
}

// ListAll returns every event stored in the database.
func (r EventRepository) ListAll(ctx context.Context) ([]domain.Event, error) {
	rows, err := r.q.ListAllEvents(ctx)
	if err != nil {
		return nil, fmt.Errorf("list all events: %w", err)
	}

	events := make([]domain.Event, 0, len(rows))
	for _, row := range rows {
		event, err := mapSQLEvent(row)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}

	return events, nil
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
	return r.q.DeleteEventByID(ctx, id)
}

// DeleteAll wipes the events table. Used by `mnemos reset`.
func (r EventRepository) DeleteAll(ctx context.Context) error {
	return r.q.DeleteAllEvents(ctx)
}

// ListByRunID returns all events that belong to the specified run.
func (r EventRepository) ListByRunID(ctx context.Context, runID string) ([]domain.Event, error) {
	rows, err := r.q.ListEventsByRunID(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("list events by run id: %w", err)
	}

	events := make([]domain.Event, 0, len(rows))
	for _, row := range rows {
		event, err := mapSQLEvent(row)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}

	return events, nil
}

func mapSQLEvent(row sqlcgen.Event) (domain.Event, error) {
	eventTimestamp, err := time.Parse(time.RFC3339Nano, row.Timestamp)
	if err != nil {
		return domain.Event{}, fmt.Errorf("parse event timestamp: %w", err)
	}
	eventIngestedAt, err := time.Parse(time.RFC3339Nano, row.IngestedAt)
	if err != nil {
		return domain.Event{}, fmt.Errorf("parse event ingested_at: %w", err)
	}
	metadata := map[string]string{}
	if err := json.Unmarshal([]byte(row.MetadataJson), &metadata); err != nil {
		return domain.Event{}, fmt.Errorf("unmarshal event metadata: %w", err)
	}

	return domain.Event{
		ID:            row.ID,
		RunID:         row.RunID,
		SchemaVersion: row.SchemaVersion,
		Content:       row.Content,
		SourceInputID: row.SourceInputID,
		Timestamp:     eventTimestamp,
		Metadata:      metadata,
		IngestedAt:    eventIngestedAt,
		CreatedBy:     row.CreatedBy,
	}, nil
}

type eventRowScanner interface {
	Scan(dest ...any) error
}

func scanEvent(scanner eventRowScanner) (domain.Event, error) {
	var (
		event       domain.Event
		timestamp   string
		ingestedAt  string
		metadataRaw string
		runID       string
	)

	if err := scanner.Scan(
		&event.ID,
		&runID,
		&event.SchemaVersion,
		&event.Content,
		&event.SourceInputID,
		&timestamp,
		&metadataRaw,
		&ingestedAt,
		&event.CreatedBy,
	); err != nil {
		return domain.Event{}, err
	}

	eventTimestamp, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return domain.Event{}, fmt.Errorf("parse event timestamp: %w", err)
	}
	event.Timestamp = eventTimestamp

	eventIngestedAt, err := time.Parse(time.RFC3339Nano, ingestedAt)
	if err != nil {
		return domain.Event{}, fmt.Errorf("parse event ingested_at: %w", err)
	}
	event.IngestedAt = eventIngestedAt

	metadata := map[string]string{}
	if err := json.Unmarshal([]byte(metadataRaw), &metadata); err != nil {
		return domain.Event{}, fmt.Errorf("unmarshal event metadata: %w", err)
	}
	event.Metadata = metadata
	event.RunID = runID

	return event, nil
}
