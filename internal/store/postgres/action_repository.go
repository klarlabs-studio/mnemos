package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ActionRepository persists operational actions in the configured
// Postgres namespace. Mirrors the SQLite contract.
type ActionRepository struct {
	db pgQuerier
	ns string
}

// Append validates and stores an action. Idempotent on id.
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
	_, err = r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (id, run_id, kind, subject, actor, at, metadata_json, created_by, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9)
ON CONFLICT (id) DO NOTHING`, qualify(r.ns, "actions")),
		action.ID,
		action.RunID,
		string(action.Kind),
		action.Subject,
		action.Actor,
		action.At.UTC(),
		string(metadata),
		actorOr(action.CreatedBy),
		createdAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("insert action: %w", err)
	}
	return nil
}

// GetByID returns the action with the given id.
func (r ActionRepository) GetByID(ctx context.Context, id string) (domain.Action, error) {
	row := r.db.QueryRowContext(ctx, fmt.Sprintf(`
SELECT id, run_id, kind, subject, actor, at, metadata_json::text, created_by, created_at
FROM %s WHERE id = $1`, qualify(r.ns, "actions")), id)
	a, err := scanActionRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Action{}, fmt.Errorf("action %s not found", id)
	}
	return a, err
}

// ListByRunID returns actions tagged with the given run id.
func (r ActionRepository) ListByRunID(ctx context.Context, runID string) ([]domain.Action, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, run_id, kind, subject, actor, at, metadata_json::text, created_by, created_at
FROM %s WHERE run_id = $1 ORDER BY at ASC`, qualify(r.ns, "actions")), runID)
	if err != nil {
		return nil, fmt.Errorf("list actions by run id: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectActionRows(rows)
}

// ListBySubject returns actions taken on the given subject.
func (r ActionRepository) ListBySubject(ctx context.Context, subject string) ([]domain.Action, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, run_id, kind, subject, actor, at, metadata_json::text, created_by, created_at
FROM %s WHERE subject = $1 ORDER BY at ASC`, qualify(r.ns, "actions")), subject)
	if err != nil {
		return nil, fmt.Errorf("list actions by subject: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectActionRows(rows)
}

// ListAll returns every action ordered by action time ascending.
func (r ActionRepository) ListAll(ctx context.Context) ([]domain.Action, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, run_id, kind, subject, actor, at, metadata_json::text, created_by, created_at
FROM %s ORDER BY at ASC`, qualify(r.ns, "actions")))
	if err != nil {
		return nil, fmt.Errorf("list all actions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectActionRows(rows)
}

// CountAll returns the total number of actions stored.
func (r ActionRepository) CountAll(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s`, qualify(r.ns, "actions"),
	)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count actions: %w", err)
	}
	return n, nil
}

// DeleteAll wipes every action row.
func (r ActionRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s`, qualify(r.ns, "actions"),
	)); err != nil {
		return fmt.Errorf("delete all actions: %w", err)
	}
	return nil
}

func scanActionRow(row *sql.Row) (domain.Action, error) {
	var a domain.Action
	var kind, metadataRaw string
	if err := row.Scan(
		&a.ID, &a.RunID, &kind, &a.Subject, &a.Actor, &a.At,
		&metadataRaw, &a.CreatedBy, &a.CreatedAt,
	); err != nil {
		return domain.Action{}, err
	}
	a.Kind = domain.ActionKind(kind)
	if err := unmarshalMetadata(metadataRaw, &a.Metadata); err != nil {
		return domain.Action{}, err
	}
	return a, nil
}

func scanActionRows(rows *sql.Rows) (domain.Action, error) {
	var a domain.Action
	var kind, metadataRaw string
	if err := rows.Scan(
		&a.ID, &a.RunID, &kind, &a.Subject, &a.Actor, &a.At,
		&metadataRaw, &a.CreatedBy, &a.CreatedAt,
	); err != nil {
		return domain.Action{}, err
	}
	a.Kind = domain.ActionKind(kind)
	if err := unmarshalMetadata(metadataRaw, &a.Metadata); err != nil {
		return domain.Action{}, err
	}
	return a, nil
}

func collectActionRows(rows *sql.Rows) ([]domain.Action, error) {
	out := make([]domain.Action, 0)
	for rows.Next() {
		a, err := scanActionRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
