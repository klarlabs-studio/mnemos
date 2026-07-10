package postgres

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

// DecisionRepository persists recorded decisions in the configured
// Postgres namespace.
type DecisionRepository struct {
	db pgQuerier
	ns string
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
	_, err = r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (id, statement, plan, reasoning, risk_level, alternatives_json, outcome_id, chosen_at, created_by, created_at, refuted_beliefs_json, failed_outcome_id)
VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10, $11::jsonb, $12)
ON CONFLICT (id) DO UPDATE SET
  statement = EXCLUDED.statement,
  plan = EXCLUDED.plan,
  reasoning = EXCLUDED.reasoning,
  risk_level = EXCLUDED.risk_level,
  alternatives_json = EXCLUDED.alternatives_json,
  outcome_id = EXCLUDED.outcome_id,
  refuted_beliefs_json = EXCLUDED.refuted_beliefs_json,
  failed_outcome_id = EXCLUDED.failed_outcome_id`, qualify(r.ns, "decisions")),
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
	row := r.db.QueryRowContext(ctx, fmt.Sprintf(`
SELECT id, statement, plan, reasoning, risk_level, alternatives_json::text, outcome_id, chosen_at, created_by, created_at, refuted_beliefs_json::text, failed_outcome_id
FROM %s WHERE id = $1`, qualify(r.ns, "decisions")), id)
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
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, statement, plan, reasoning, risk_level, alternatives_json::text, outcome_id, chosen_at, created_by, created_at, refuted_beliefs_json::text, failed_outcome_id
FROM %s ORDER BY chosen_at DESC`, qualify(r.ns, "decisions")))
	if err != nil {
		return nil, fmt.Errorf("list all decisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return r.collectDecisionRows(ctx, rows)
}

// ListByRiskLevel returns decisions filtered by risk level.
func (r DecisionRepository) ListByRiskLevel(ctx context.Context, level string) ([]domain.Decision, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, statement, plan, reasoning, risk_level, alternatives_json::text, outcome_id, chosen_at, created_by, created_at, refuted_beliefs_json::text, failed_outcome_id
FROM %s WHERE risk_level = $1 ORDER BY chosen_at DESC`, qualify(r.ns, "decisions")), level)
	if err != nil {
		return nil, fmt.Errorf("list decisions by risk_level: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return r.collectDecisionRows(ctx, rows)
}

// AttachOutcome wires an outcome id onto an existing decision.
func (r DecisionRepository) AttachOutcome(ctx context.Context, decisionID, outcomeID string) error {
	if _, err := r.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET outcome_id = $1 WHERE id = $2`, qualify(r.ns, "decisions"),
	), outcomeID, decisionID); err != nil {
		return fmt.Errorf("attach outcome: %w", err)
	}
	return nil
}

// CountAll returns the total number of decisions stored.
func (r DecisionRepository) CountAll(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s`, qualify(r.ns, "decisions"),
	)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count decisions: %w", err)
	}
	return n, nil
}

// DeleteAll wipes decisions + decision_beliefs.
func (r DecisionRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s`, qualify(r.ns, "decision_beliefs"),
	)); err != nil {
		return fmt.Errorf("delete all decision beliefs: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s`, qualify(r.ns, "decisions"),
	)); err != nil {
		return fmt.Errorf("delete all decisions: %w", err)
	}
	return nil
}

// AppendBeliefs is idempotent on (decision_id, claim_id).
func (r DecisionRepository) AppendBeliefs(ctx context.Context, decisionID string, claimIDs []string) error {
	for _, cid := range claimIDs {
		if _, err := r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (decision_id, claim_id) VALUES ($1, $2)
ON CONFLICT (decision_id, claim_id) DO NOTHING`, qualify(r.ns, "decision_beliefs")), decisionID, cid); err != nil {
			return fmt.Errorf("append decision belief: %w", err)
		}
	}
	return nil
}

// ListBeliefs returns the claim ids that backed the decision.
func (r DecisionRepository) ListBeliefs(ctx context.Context, decisionID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT claim_id FROM %s WHERE decision_id = $1 ORDER BY claim_id`,
		qualify(r.ns, "decision_beliefs"),
	), decisionID)
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
