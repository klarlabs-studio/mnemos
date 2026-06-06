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

// DecisionRepository persists recorded decisions in the MySQL backend.
type DecisionRepository struct {
	db *sql.DB
}

// Append upserts a decision and seeds belief link rows.
func (r DecisionRepository) Append(ctx context.Context, decision domain.Decision) error {
	if err := decision.Validate(); err != nil {
		return fmt.Errorf("invalid decision: %w", err)
	}
	alts, err := json.Marshal(decision.Alternatives)
	if err != nil {
		return fmt.Errorf("marshal decision alternatives: %w", err)
	}
	refuted, err := json.Marshal(decision.RefutedBeliefs)
	if err != nil {
		return fmt.Errorf("marshal decision refuted_beliefs: %w", err)
	}
	createdAt := decision.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO decisions (id, statement, plan, reasoning, risk_level, alternatives_json, outcome_id, chosen_at, created_by, created_at, refuted_beliefs_json, failed_outcome_id)
VALUES (?, ?, ?, ?, ?, CAST(? AS JSON), ?, ?, ?, ?, CAST(? AS JSON), ?)
ON DUPLICATE KEY UPDATE
  statement = VALUES(statement),
  plan = VALUES(plan),
  reasoning = VALUES(reasoning),
  risk_level = VALUES(risk_level),
  alternatives_json = VALUES(alternatives_json),
  outcome_id = VALUES(outcome_id),
  refuted_beliefs_json = VALUES(refuted_beliefs_json),
  failed_outcome_id = VALUES(failed_outcome_id)`,
		decision.ID, decision.Statement, decision.Plan, decision.Reasoning,
		string(decision.RiskLevel), string(alts),
		decision.OutcomeID,
		decision.ChosenAt.UTC(),
		actorOr(decision.CreatedBy),
		createdAt.UTC(),
		string(refuted),
		decision.FailedOutcomeID,
	)
	if err != nil {
		return fmt.Errorf("insert decision: %w", err)
	}
	if len(decision.Beliefs) > 0 {
		if err := r.AppendBeliefs(ctx, decision.ID, decision.Beliefs); err != nil {
			return err
		}
	}
	return nil
}

// GetByID returns the decision plus its beliefs.
func (r DecisionRepository) GetByID(ctx context.Context, id string) (domain.Decision, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, statement, plan, reasoning, risk_level, alternatives_json, outcome_id, chosen_at, created_by, created_at, refuted_beliefs_json, failed_outcome_id
FROM decisions WHERE id = ?`, id)
	d, err := scanDecisionRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Decision{}, fmt.Errorf("decision %s not found", id)
	}
	if err != nil {
		return domain.Decision{}, err
	}
	bs, err := r.ListBeliefs(ctx, id)
	if err != nil {
		return domain.Decision{}, err
	}
	d.Beliefs = bs
	return d, nil
}

// ListAll returns every decision newest-first.
func (r DecisionRepository) ListAll(ctx context.Context) ([]domain.Decision, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, statement, plan, reasoning, risk_level, alternatives_json, outcome_id, chosen_at, created_by, created_at, refuted_beliefs_json, failed_outcome_id
FROM decisions ORDER BY chosen_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list all decisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return r.collectDecisionRows(ctx, rows)
}

// ListByRiskLevel returns decisions filtered by risk level.
func (r DecisionRepository) ListByRiskLevel(ctx context.Context, level string) ([]domain.Decision, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, statement, plan, reasoning, risk_level, alternatives_json, outcome_id, chosen_at, created_by, created_at, refuted_beliefs_json, failed_outcome_id
FROM decisions WHERE risk_level = ? ORDER BY chosen_at DESC`, level)
	if err != nil {
		return nil, fmt.Errorf("list decisions by risk_level: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return r.collectDecisionRows(ctx, rows)
}

// AttachOutcome wires an outcome id onto an existing decision.
func (r DecisionRepository) AttachOutcome(ctx context.Context, decisionID, outcomeID string) error {
	if _, err := r.db.ExecContext(ctx,
		`UPDATE decisions SET outcome_id = ? WHERE id = ?`,
		outcomeID, decisionID,
	); err != nil {
		return fmt.Errorf("attach outcome: %w", err)
	}
	return nil
}

// CountAll returns the total number of decisions stored.
func (r DecisionRepository) CountAll(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM decisions`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count decisions: %w", err)
	}
	return n, nil
}

// DeleteAll wipes decisions + decision_beliefs.
func (r DecisionRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM decision_beliefs`); err != nil {
		return fmt.Errorf("delete all decision beliefs: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM decisions`); err != nil {
		return fmt.Errorf("delete all decisions: %w", err)
	}
	return nil
}

// AppendBeliefs is idempotent on (decision_id, claim_id).
func (r DecisionRepository) AppendBeliefs(ctx context.Context, decisionID string, claimIDs []string) error {
	for _, cid := range claimIDs {
		if _, err := r.db.ExecContext(ctx,
			`INSERT IGNORE INTO decision_beliefs (decision_id, claim_id) VALUES (?, ?)`,
			decisionID, cid,
		); err != nil {
			return fmt.Errorf("append decision belief: %w", err)
		}
	}
	return nil
}

// ListBeliefs returns the claim ids that backed the decision.
func (r DecisionRepository) ListBeliefs(ctx context.Context, decisionID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT claim_id FROM decision_beliefs WHERE decision_id = ? ORDER BY claim_id`,
		decisionID,
	)
	if err != nil {
		return nil, fmt.Errorf("list decision beliefs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]string, 0)
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			return nil, err
		}
		out = append(out, cid)
	}
	return out, rows.Err()
}

func (r DecisionRepository) collectDecisionRows(ctx context.Context, rows *sql.Rows) ([]domain.Decision, error) {
	out := make([]domain.Decision, 0)
	for rows.Next() {
		d, err := scanDecisionRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		bs, err := r.ListBeliefs(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Beliefs = bs
	}
	return out, nil
}

func scanDecisionRow(row *sql.Row) (domain.Decision, error) {
	var d domain.Decision
	var risk, altsRaw, refutedRaw string
	if err := row.Scan(
		&d.ID, &d.Statement, &d.Plan, &d.Reasoning, &risk,
		&altsRaw, &d.OutcomeID, &d.ChosenAt, &d.CreatedBy, &d.CreatedAt,
		&refutedRaw, &d.FailedOutcomeID,
	); err != nil {
		return domain.Decision{}, err
	}
	d.RiskLevel = domain.RiskLevel(risk)
	if err := unmarshalStrings(altsRaw, &d.Alternatives); err != nil {
		return domain.Decision{}, err
	}
	if err := unmarshalStrings(refutedRaw, &d.RefutedBeliefs); err != nil {
		return domain.Decision{}, err
	}
	return d, nil
}

func scanDecisionRows(rows *sql.Rows) (domain.Decision, error) {
	var d domain.Decision
	var risk, altsRaw, refutedRaw string
	if err := rows.Scan(
		&d.ID, &d.Statement, &d.Plan, &d.Reasoning, &risk,
		&altsRaw, &d.OutcomeID, &d.ChosenAt, &d.CreatedBy, &d.CreatedAt,
		&refutedRaw, &d.FailedOutcomeID,
	); err != nil {
		return domain.Decision{}, err
	}
	d.RiskLevel = domain.RiskLevel(risk)
	if err := unmarshalStrings(altsRaw, &d.Alternatives); err != nil {
		return domain.Decision{}, err
	}
	if err := unmarshalStrings(refutedRaw, &d.RefutedBeliefs); err != nil {
		return domain.Decision{}, err
	}
	return d, nil
}

func unmarshalStrings(raw string, dst *[]string) error {
	if strings.TrimSpace(raw) == "" || raw == "null" {
		*dst = nil
		return nil
	}
	out := []string{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return fmt.Errorf("unmarshal string slice: %w", err)
	}
	*dst = out
	return nil
}
