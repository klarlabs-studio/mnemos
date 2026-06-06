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

// OutcomeRepository provides SQLite-backed storage for action outcomes.
type OutcomeRepository struct {
	db *sql.DB
	q  *sqlcgen.Queries
}

// NewOutcomeRepository returns an OutcomeRepository backed by db.
func NewOutcomeRepository(db *sql.DB) OutcomeRepository {
	return OutcomeRepository{db: db, q: sqlcgen.New(db)}
}

// Append persists an outcome. Idempotent on id.
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
	return r.q.CreateOutcome(ctx, sqlcgen.CreateOutcomeParams{
		ID:          outcome.ID,
		ActionID:    outcome.ActionID,
		Result:      string(outcome.Result),
		MetricsJson: string(metrics),
		Notes:       outcome.Notes,
		ObservedAt:  outcome.ObservedAt.UTC().Format(time.RFC3339Nano),
		Source:      source,
		CreatedBy:   actorOr(outcome.CreatedBy),
		CreatedAt:   createdAt.UTC().Format(time.RFC3339Nano),
	})
}

// GetByID retrieves a single outcome by id.
func (r OutcomeRepository) GetByID(ctx context.Context, id string) (domain.Outcome, error) {
	row, err := r.q.GetOutcomeByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Outcome{}, fmt.Errorf("outcome %s not found", id)
	}
	if err != nil {
		return domain.Outcome{}, err
	}
	return mapSQLOutcome(row)
}

// ListByActionID returns every outcome reported for the given action.
func (r OutcomeRepository) ListByActionID(ctx context.Context, actionID string) ([]domain.Outcome, error) {
	rows, err := r.q.ListOutcomesByActionID(ctx, actionID)
	if err != nil {
		return nil, err
	}
	return mapOutcomes(rows)
}

// ListAll returns every outcome stored, ordered by observed_at
// ascending.
func (r OutcomeRepository) ListAll(ctx context.Context) ([]domain.Outcome, error) {
	rows, err := r.q.ListAllOutcomes(ctx)
	if err != nil {
		return nil, err
	}
	return mapOutcomes(rows)
}

// CountAll returns the total number of outcomes stored.
func (r OutcomeRepository) CountAll(ctx context.Context) (int64, error) {
	return r.q.CountOutcomes(ctx)
}

// DeleteAll wipes every outcome row.
func (r OutcomeRepository) DeleteAll(ctx context.Context) error {
	return r.q.DeleteAllOutcomes(ctx)
}

func mapOutcomes(rows []sqlcgen.Outcome) ([]domain.Outcome, error) {
	out := make([]domain.Outcome, 0, len(rows))
	for _, r := range rows {
		o, err := mapSQLOutcome(r)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func mapSQLOutcome(row sqlcgen.Outcome) (domain.Outcome, error) {
	observedAt, err := time.Parse(time.RFC3339Nano, row.ObservedAt)
	if err != nil {
		return domain.Outcome{}, fmt.Errorf("parse outcome.observed_at: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, row.CreatedAt)
	if err != nil {
		return domain.Outcome{}, fmt.Errorf("parse outcome.created_at: %w", err)
	}
	var metrics map[string]float64
	if row.MetricsJson != "" {
		if err := json.Unmarshal([]byte(row.MetricsJson), &metrics); err != nil {
			return domain.Outcome{}, fmt.Errorf("unmarshal outcome.metrics_json: %w", err)
		}
	}
	return domain.Outcome{
		ID:         row.ID,
		ActionID:   row.ActionID,
		Result:     domain.OutcomeResult(row.Result),
		Metrics:    metrics,
		Notes:      row.Notes,
		ObservedAt: observedAt,
		Source:     row.Source,
		CreatedBy:  row.CreatedBy,
		CreatedAt:  createdAt,
	}, nil
}
