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

// DecisionRepository provides SQLite-backed storage for recorded
// decisions and their belief link rows.
type DecisionRepository struct {
	db *sql.DB
	q  *sqlcgen.Queries
}

// NewDecisionRepository returns a DecisionRepository backed by db.
func NewDecisionRepository(db *sql.DB) DecisionRepository {
	return DecisionRepository{db: db, q: sqlcgen.New(db)}
}

// Append upserts the decision row and seeds its belief links.
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
	if err := r.q.CreateDecision(ctx, sqlcgen.CreateDecisionParams{
		ID:                 decision.ID,
		Statement:          decision.Statement,
		Plan:               decision.Plan,
		Reasoning:          decision.Reasoning,
		RiskLevel:          string(decision.RiskLevel),
		AlternativesJson:   string(alts),
		OutcomeID:          decision.OutcomeID,
		ChosenAt:           decision.ChosenAt.UTC().Format(time.RFC3339Nano),
		CreatedBy:          actorOr(decision.CreatedBy),
		CreatedAt:          createdAt.UTC().Format(time.RFC3339Nano),
		ScopeService:       decision.Scope.Service,
		ScopeEnv:           decision.Scope.Env,
		ScopeTeam:          decision.Scope.Team,
		RefutedBeliefsJson: string(refuted),
		FailedOutcomeID:    decision.FailedOutcomeID,
	}); err != nil {
		return fmt.Errorf("insert decision: %w", err)
	}
	if len(decision.Beliefs) > 0 {
		if err := r.AppendBeliefs(ctx, decision.ID, decision.Beliefs); err != nil {
			return err
		}
	}
	return nil
}

// GetByID returns the decision plus its belief claim ids.
func (r DecisionRepository) GetByID(ctx context.Context, id string) (domain.Decision, error) {
	row, err := r.q.GetDecisionByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Decision{}, fmt.Errorf("decision %s not found", id)
	}
	if err != nil {
		return domain.Decision{}, err
	}
	d, err := mapSQLDecision(row)
	if err != nil {
		return domain.Decision{}, err
	}
	beliefs, err := r.ListBeliefs(ctx, id)
	if err != nil {
		return domain.Decision{}, err
	}
	d.Beliefs = beliefs
	return d, nil
}

// ListAll returns every decision newest-first by chosen_at.
func (r DecisionRepository) ListAll(ctx context.Context) ([]domain.Decision, error) {
	rows, err := r.q.ListAllDecisions(ctx)
	if err != nil {
		return nil, err
	}
	return r.hydrateDecisions(ctx, rows)
}

// ListByRiskLevel returns decisions matching the given risk level.
func (r DecisionRepository) ListByRiskLevel(ctx context.Context, level string) ([]domain.Decision, error) {
	rows, err := r.q.ListDecisionsByRiskLevel(ctx, level)
	if err != nil {
		return nil, err
	}
	return r.hydrateDecisions(ctx, rows)
}

// AttachOutcome wires an outcome id onto the decision so the audit
// path can replay belief -> action -> outcome with one repository
// fetch.
func (r DecisionRepository) AttachOutcome(ctx context.Context, decisionID, outcomeID string) error {
	return r.q.AttachDecisionOutcome(ctx, sqlcgen.AttachDecisionOutcomeParams{
		OutcomeID: outcomeID,
		ID:        decisionID,
	})
}

// CountAll returns the total number of decisions stored.
func (r DecisionRepository) CountAll(ctx context.Context) (int64, error) {
	return r.q.CountDecisions(ctx)
}

// DeleteAll wipes decisions + decision_beliefs.
func (r DecisionRepository) DeleteAll(ctx context.Context) error {
	if err := r.q.DeleteAllDecisionBeliefs(ctx); err != nil {
		return fmt.Errorf("delete all decision beliefs: %w", err)
	}
	return r.q.DeleteAllDecisions(ctx)
}

// AppendBeliefs is idempotent on (decision_id, claim_id).
func (r DecisionRepository) AppendBeliefs(ctx context.Context, decisionID string, claimIDs []string) error {
	for _, cid := range claimIDs {
		if err := r.q.AppendDecisionBelief(ctx, sqlcgen.AppendDecisionBeliefParams{
			DecisionID: decisionID,
			ClaimID:    cid,
		}); err != nil {
			return fmt.Errorf("append decision belief: %w", err)
		}
	}
	return nil
}

// ListBeliefs returns the claim ids that backed the decision.
func (r DecisionRepository) ListBeliefs(ctx context.Context, decisionID string) ([]string, error) {
	rows, err := r.q.ListDecisionBeliefs(ctx, decisionID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ClaimID)
	}
	return out, nil
}

func (r DecisionRepository) hydrateDecisions(ctx context.Context, rows []sqlcgen.Decision) ([]domain.Decision, error) {
	out := make([]domain.Decision, 0, len(rows))
	for _, row := range rows {
		d, err := mapSQLDecision(row)
		if err != nil {
			return nil, err
		}
		bs, err := r.ListBeliefs(ctx, d.ID)
		if err != nil {
			return nil, err
		}
		d.Beliefs = bs
		out = append(out, d)
	}
	return out, nil
}

func mapSQLDecision(row sqlcgen.Decision) (domain.Decision, error) {
	chosen, err := time.Parse(time.RFC3339Nano, row.ChosenAt)
	if err != nil {
		return domain.Decision{}, fmt.Errorf("parse decision.chosen_at: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, row.CreatedAt)
	if err != nil {
		return domain.Decision{}, fmt.Errorf("parse decision.created_at: %w", err)
	}
	var alts []string
	if row.AlternativesJson != "" {
		if err := json.Unmarshal([]byte(row.AlternativesJson), &alts); err != nil {
			return domain.Decision{}, fmt.Errorf("unmarshal decision.alternatives_json: %w", err)
		}
	}
	var refuted []string
	src := row.RefutedBeliefsJson
	if src == "" {
		src = "[]"
	}
	if err := json.Unmarshal([]byte(src), &refuted); err != nil {
		return domain.Decision{}, fmt.Errorf("unmarshal decision.refuted_beliefs_json: %w", err)
	}
	return domain.Decision{
		ID:           row.ID,
		Statement:    row.Statement,
		Plan:         row.Plan,
		Reasoning:    row.Reasoning,
		RiskLevel:    domain.RiskLevel(row.RiskLevel),
		Alternatives: alts,
		OutcomeID:    row.OutcomeID,
		ChosenAt:     chosen,
		CreatedBy:    row.CreatedBy,
		CreatedAt:    createdAt,
		Scope: domain.Scope{
			Service: row.ScopeService,
			Env:     row.ScopeEnv,
			Team:    row.ScopeTeam,
		},
		RefutedBeliefs:  refuted,
		FailedOutcomeID: row.FailedOutcomeID,
	}, nil
}
