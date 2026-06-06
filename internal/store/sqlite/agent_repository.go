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

// AgentRepository persists and retrieves non-human principals.
// Backed by sqlc-generated queries (see sql/sqlite/query/agents.sql).
// Scopes and allowed_runs are JSON-encoded TEXT to keep the column
// scannable from shell SQL one-liners without a bespoke decoder.
type AgentRepository struct {
	db *sql.DB
	q  *sqlcgen.Queries
}

// NewAgentRepository returns an AgentRepository backed by the given database.
func NewAgentRepository(db *sql.DB) AgentRepository {
	return AgentRepository{db: db, q: sqlcgen.New(db)}
}

// Create inserts a new agent. Owner must already exist (FK
// constraint); upstream callers should look the user up first to
// produce a friendlier error than the FK violation.
func (r AgentRepository) Create(ctx context.Context, a domain.Agent) error {
	if err := a.Validate(); err != nil {
		return fmt.Errorf("invalid agent: %w", err)
	}
	scopesJSON, err := encodeScopes(a.Scopes)
	if err != nil {
		return err
	}
	runsJSON, err := encodeScopes(a.AllowedRuns)
	if err != nil {
		return err
	}
	if err := r.q.CreateAgent(ctx, sqlcgen.CreateAgentParams{
		ID:              a.ID,
		Name:            a.Name,
		OwnerID:         a.OwnerID,
		ScopesJson:      scopesJSON,
		AllowedRunsJson: runsJSON,
		Status:          string(a.Status),
		CreatedAt:       a.CreatedAt.UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return fmt.Errorf("insert agent %s: %w", a.ID, err)
	}
	return nil
}

// GetByID returns the agent with the given ID, or sql.ErrNoRows.
func (r AgentRepository) GetByID(ctx context.Context, id string) (domain.Agent, error) {
	row, err := r.q.GetAgentByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Agent{}, fmt.Errorf("agent %s: %w", id, sql.ErrNoRows)
		}
		return domain.Agent{}, fmt.Errorf("get agent %s: %w", id, err)
	}
	return rowToAgent(row)
}

// List returns every agent in created_at order (oldest first).
func (r AgentRepository) List(ctx context.Context) ([]domain.Agent, error) {
	rows, err := r.q.ListAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	out := make([]domain.Agent, 0, len(rows))
	for _, row := range rows {
		a, err := rowToAgent(row)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// UpdateStatus changes an agent's status (e.g., active → revoked).
func (r AgentRepository) UpdateStatus(ctx context.Context, id string, status domain.AgentStatus) error {
	if err := r.q.UpdateAgentStatus(ctx, sqlcgen.UpdateAgentStatusParams{
		Status: string(status),
		ID:     id,
	}); err != nil {
		return fmt.Errorf("update agent status %s: %w", id, err)
	}
	return r.assertExists(ctx, id)
}

// UpdateScopes replaces the agent's scope list. In-flight tokens
// keep their issued scopes; only freshly-issued tokens see the new
// list.
func (r AgentRepository) UpdateScopes(ctx context.Context, id string, scopes []string) error {
	scopesJSON, err := encodeScopes(scopes)
	if err != nil {
		return err
	}
	if err := r.q.UpdateAgentScopes(ctx, sqlcgen.UpdateAgentScopesParams{
		ScopesJson: scopesJSON,
		ID:         id,
	}); err != nil {
		return fmt.Errorf("update agent scopes %s: %w", id, err)
	}
	return r.assertExists(ctx, id)
}

// UpdateAllowedRuns replaces the agent's run whitelist.
func (r AgentRepository) UpdateAllowedRuns(ctx context.Context, id string, runs []string) error {
	runsJSON, err := encodeScopes(runs)
	if err != nil {
		return err
	}
	if err := r.q.UpdateAgentAllowedRuns(ctx, sqlcgen.UpdateAgentAllowedRunsParams{
		AllowedRunsJson: runsJSON,
		ID:              id,
	}); err != nil {
		return fmt.Errorf("update agent allowed_runs %s: %w", id, err)
	}
	return r.assertExists(ctx, id)
}

// Upsert idempotently writes a batch of agents. Used by federation
// (push/pull) so a registry mirrors peers' agents while preserving
// identity, status, scopes, and allowed-runs.
func (r AgentRepository) Upsert(ctx context.Context, agents []domain.Agent) error {
	if len(agents) == 0 {
		return nil
	}
	for _, a := range agents {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("invalid agent %s: %w", a.ID, err)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("upsert agents: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := r.q.WithTx(tx)
	for _, a := range agents {
		scopesJSON, err := encodeScopes(a.Scopes)
		if err != nil {
			return err
		}
		runsJSON, err := encodeScopes(a.AllowedRuns)
		if err != nil {
			return err
		}
		if err := q.UpsertAgent(ctx, sqlcgen.UpsertAgentParams{
			ID:              a.ID,
			Name:            a.Name,
			OwnerID:         a.OwnerID,
			ScopesJson:      scopesJSON,
			AllowedRunsJson: runsJSON,
			Status:          string(a.Status),
			CreatedAt:       a.CreatedAt.UTC().Format(time.RFC3339Nano),
		}); err != nil {
			return fmt.Errorf("upsert agent %s: %w", a.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("upsert agents: commit: %w", err)
	}
	return nil
}

// assertExists returns sql.ErrNoRows when the id is unknown so the
// Update* methods preserve their established "missing row → ErrNoRows"
// contract that sqlc's :exec doesn't surface on its own.
func (r AgentRepository) assertExists(ctx context.Context, id string) error {
	if _, err := r.q.GetAgentByID(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("agent %s: %w", id, sql.ErrNoRows)
		}
		return err
	}
	return nil
}

func encodeScopes(scopes []string) (string, error) {
	if scopes == nil {
		scopes = []string{}
	}
	b, err := json.Marshal(scopes)
	if err != nil {
		return "", fmt.Errorf("encode agent scopes: %w", err)
	}
	return string(b), nil
}

func rowToAgent(row sqlcgen.Agent) (domain.Agent, error) {
	t, err := time.Parse(time.RFC3339Nano, row.CreatedAt)
	if err != nil {
		return domain.Agent{}, fmt.Errorf("parse agent created_at: %w", err)
	}
	a := domain.Agent{
		ID:        row.ID,
		Name:      row.Name,
		OwnerID:   row.OwnerID,
		Status:    domain.AgentStatus(row.Status),
		CreatedAt: t,
	}
	if err := json.Unmarshal([]byte(row.ScopesJson), &a.Scopes); err != nil {
		return domain.Agent{}, fmt.Errorf("decode agent scopes: %w", err)
	}
	if err := json.Unmarshal([]byte(row.AllowedRunsJson), &a.AllowedRuns); err != nil {
		return domain.Agent{}, fmt.Errorf("decode agent allowed_runs: %w", err)
	}
	return a, nil
}
