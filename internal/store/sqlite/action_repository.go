package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store/sqlite/sqlcgen"
)

// ActionRepository provides SQLite-backed storage for operational
// actions. Mirrors the EventRepository pattern: thin wrapper over
// sqlc-generated queries plus the JSON marshalling sqlc can't do for
// us.
type ActionRepository struct {
	db *sql.DB
	q  *sqlcgen.Queries
}

// NewActionRepository returns an ActionRepository backed by db.
func NewActionRepository(db *sql.DB) ActionRepository {
	return ActionRepository{db: db, q: sqlcgen.New(db)}
}

// Append persists an action. Idempotent on the primary key —
// double-writing the same action id is a no-op.
func (r ActionRepository) Append(ctx context.Context, action domain.Action) error {
	if err := action.Validate(); err != nil {
		return fmt.Errorf("invalid action: %w", err)
	}
	metadata, err := json.Marshal(action.Metadata)
	if err != nil {
		return fmt.Errorf("marshal action metadata: %w", err)
	}
	createdAt := action.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return r.q.CreateAction(ctx, sqlcgen.CreateActionParams{
		ID:           action.ID,
		RunID:        action.RunID,
		Kind:         string(action.Kind),
		Subject:      action.Subject,
		Actor:        action.Actor,
		At:           action.At.UTC().Format(time.RFC3339Nano),
		MetadataJson: string(metadata),
		CreatedBy:    actorOr(action.CreatedBy),
		CreatedAt:    createdAt.UTC().Format(time.RFC3339Nano),
	})
}

// GetByID retrieves a single action by id.
func (r ActionRepository) GetByID(ctx context.Context, id string) (domain.Action, error) {
	row, err := r.q.GetActionByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Action{}, fmt.Errorf("action %s not found", id)
	}
	if err != nil {
		return domain.Action{}, err
	}
	return mapSQLAction(row)
}

// ListByRunID returns all actions tagged with the given run id.
func (r ActionRepository) ListByRunID(ctx context.Context, runID string) ([]domain.Action, error) {
	rows, err := r.q.ListActionsByRunID(ctx, runID)
	if err != nil {
		return nil, err
	}
	return mapActions(rows)
}

// ListBySubject returns all actions taken on the given subject (e.g.
// service or component name). Used by lessons synthesis to cluster
// per-subject action -> outcome chains.
func (r ActionRepository) ListBySubject(ctx context.Context, subject string) ([]domain.Action, error) {
	rows, err := r.q.ListActionsBySubject(ctx, subject)
	if err != nil {
		return nil, err
	}
	return mapActions(rows)
}

// ListAll returns every action stored, ordered by action time
// ascending.
func (r ActionRepository) ListAll(ctx context.Context) ([]domain.Action, error) {
	rows, err := r.q.ListAllActions(ctx)
	if err != nil {
		return nil, err
	}
	return mapActions(rows)
}

// CountAll returns the total number of actions stored.
func (r ActionRepository) CountAll(ctx context.Context) (int64, error) {
	return r.q.CountActions(ctx)
}

// DeleteAll wipes every action row. Outcomes referencing actions must
// be cleared first; the FK in the schema enforces this contract.
func (r ActionRepository) DeleteAll(ctx context.Context) error {
	return r.q.DeleteAllActions(ctx)
}

func mapActions(rows []sqlcgen.Action) ([]domain.Action, error) {
	out := make([]domain.Action, 0, len(rows))
	for _, r := range rows {
		a, err := mapSQLAction(r)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func mapSQLAction(row sqlcgen.Action) (domain.Action, error) {
	at, err := time.Parse(time.RFC3339Nano, row.At)
	if err != nil {
		return domain.Action{}, fmt.Errorf("parse action.at: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, row.CreatedAt)
	if err != nil {
		return domain.Action{}, fmt.Errorf("parse action.created_at: %w", err)
	}
	var metadata map[string]string
	if row.MetadataJson != "" {
		if err := json.Unmarshal([]byte(row.MetadataJson), &metadata); err != nil {
			return domain.Action{}, fmt.Errorf("unmarshal action.metadata_json: %w", err)
		}
	}
	return domain.Action{
		ID:        row.ID,
		RunID:     row.RunID,
		Kind:      domain.ActionKind(row.Kind),
		Subject:   row.Subject,
		Actor:     row.Actor,
		At:        at,
		Metadata:  metadata,
		CreatedBy: row.CreatedBy,
		CreatedAt: createdAt,
	}, nil
}
