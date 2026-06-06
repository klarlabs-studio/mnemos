package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// OutcomeRepository implements ports.OutcomeRepository against MySQL.
type OutcomeRepository struct {
	db *sql.DB
}

// Append validates and stores an outcome. Idempotent on id.
func (r OutcomeRepository) Append(ctx context.Context, outcome domain.Outcome) error {
	if err := outcome.Validate(); err != nil {
		return fmt.Errorf("invalid outcome: %w", err)
	}
	metrics, err := json.Marshal(outcome.Metrics)
	if err != nil {
		return fmt.Errorf("marshal outcome metrics: %w", err)
	}
	source := outcome.Source
	if source == "" {
		source = "push"
	}
	createdAt := outcome.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err = r.db.ExecContext(ctx, `
INSERT IGNORE INTO outcomes (id, action_id, result, metrics_json, notes, observed_at, source, created_by, created_at)
VALUES (?, ?, ?, CAST(? AS JSON), ?, ?, ?, ?, ?)`,
		outcome.ID,
		outcome.ActionID,
		string(outcome.Result),
		string(metrics),
		outcome.Notes,
		outcome.ObservedAt.UTC(),
		source,
		actorOr(outcome.CreatedBy),
		createdAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("insert outcome: %w", err)
	}
	return nil
}

// GetByID returns the outcome with the given id.
func (r OutcomeRepository) GetByID(ctx context.Context, id string) (domain.Outcome, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, action_id, result, metrics_json, notes, observed_at, source, created_by, created_at
FROM outcomes WHERE id = ?`, id)
	o, err := scanOutcomeRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Outcome{}, fmt.Errorf("outcome %s not found", id)
	}
	return o, err
}

// ListByActionID returns outcomes for the given action id.
func (r OutcomeRepository) ListByActionID(ctx context.Context, actionID string) ([]domain.Outcome, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, action_id, result, metrics_json, notes, observed_at, source, created_by, created_at
FROM outcomes WHERE action_id = ? ORDER BY observed_at ASC`, actionID)
	if err != nil {
		return nil, fmt.Errorf("list outcomes by action id: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectOutcomeRows(rows)
}

// ListAll returns every outcome ordered by observed_at ascending.
func (r OutcomeRepository) ListAll(ctx context.Context) ([]domain.Outcome, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, action_id, result, metrics_json, notes, observed_at, source, created_by, created_at
FROM outcomes ORDER BY observed_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list all outcomes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectOutcomeRows(rows)
}

// CountAll returns the total number of outcomes stored.
func (r OutcomeRepository) CountAll(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outcomes`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count outcomes: %w", err)
	}
	return n, nil
}

// DeleteAll wipes every outcome row.
func (r OutcomeRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM outcomes`); err != nil {
		return fmt.Errorf("delete all outcomes: %w", err)
	}
	return nil
}

func scanOutcomeRow(row *sql.Row) (domain.Outcome, error) {
	var o domain.Outcome
	var result, metricsRaw string
	if err := row.Scan(
		&o.ID, &o.ActionID, &result, &metricsRaw, &o.Notes,
		&o.ObservedAt, &o.Source, &o.CreatedBy, &o.CreatedAt,
	); err != nil {
		return domain.Outcome{}, err
	}
	o.Result = domain.OutcomeResult(result)
	if err := unmarshalFloatMap(metricsRaw, &o.Metrics); err != nil {
		return domain.Outcome{}, err
	}
	return o, nil
}

func scanOutcomeRows(rows *sql.Rows) (domain.Outcome, error) {
	var o domain.Outcome
	var result, metricsRaw string
	if err := rows.Scan(
		&o.ID, &o.ActionID, &result, &metricsRaw, &o.Notes,
		&o.ObservedAt, &o.Source, &o.CreatedBy, &o.CreatedAt,
	); err != nil {
		return domain.Outcome{}, err
	}
	o.Result = domain.OutcomeResult(result)
	if err := unmarshalFloatMap(metricsRaw, &o.Metrics); err != nil {
		return domain.Outcome{}, err
	}
	return o, nil
}

func collectOutcomeRows(rows *sql.Rows) ([]domain.Outcome, error) {
	out := make([]domain.Outcome, 0)
	for rows.Next() {
		o, err := scanOutcomeRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func unmarshalStringMap(raw string, dst *map[string]string) error {
	if strings.TrimSpace(raw) == "" || raw == "null" {
		*dst = map[string]string{}
		return nil
	}
	m := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return fmt.Errorf("unmarshal string map: %w", err)
	}
	*dst = m
	return nil
}

func unmarshalFloatMap(raw string, dst *map[string]float64) error {
	if strings.TrimSpace(raw) == "" || raw == "null" {
		*dst = map[string]float64{}
		return nil
	}
	m := map[string]float64{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return fmt.Errorf("unmarshal float map: %w", err)
	}
	*dst = m
	return nil
}
