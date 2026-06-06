package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// AgentRepository persists non-human principals. Owner FK is
// enforced by the schema, so passing an unknown owner_id surfaces
// as a Postgres FK violation — same loud failure shape as SQLite.
type AgentRepository struct {
	db *sql.DB
	ns string
}

// Create inserts a new agent. Returns an error if the owner FK
// constraint fails (unknown owner_id) — same loud-failure shape as
// the SQLite implementation.
func (r AgentRepository) Create(ctx context.Context, a domain.Agent) error {
	if err := a.Validate(); err != nil {
		return fmt.Errorf("invalid agent: %w", err)
	}
	scopesJSON, err := encodeStringList(a.Scopes)
	if err != nil {
		return err
	}
	runsJSON, err := encodeStringList(a.AllowedRuns)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (id, name, owner_id, scopes_json, allowed_runs_json, status, created_at)
VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $7)`, qualify(r.ns, "agents")),
		a.ID, a.Name, a.OwnerID, scopesJSON, runsJSON, string(a.Status), a.CreatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("insert agent %s: %w", a.ID, err)
	}
	return nil
}

// GetByID satisfies the corresponding ports method.
func (r AgentRepository) GetByID(ctx context.Context, id string) (domain.Agent, error) {
	row := r.db.QueryRowContext(ctx, fmt.Sprintf(`
SELECT id, name, owner_id, scopes_json::text, allowed_runs_json::text, status, created_at FROM %s WHERE id = $1`, qualify(r.ns, "agents")), id)
	a, err := scanAgentRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Agent{}, fmt.Errorf("agent %s: %w", id, sql.ErrNoRows)
	}
	return a, err
}

// List satisfies the corresponding ports method.
func (r AgentRepository) List(ctx context.Context) ([]domain.Agent, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, name, owner_id, scopes_json::text, allowed_runs_json::text, status, created_at FROM %s ORDER BY created_at ASC`, qualify(r.ns, "agents")))
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]domain.Agent, 0)
	for rows.Next() {
		a, err := scanAgentRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateStatus satisfies the corresponding ports method.
func (r AgentRepository) UpdateStatus(ctx context.Context, id string, status domain.AgentStatus) error {
	res, err := r.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET status = $1 WHERE id = $2`, qualify(r.ns, "agents")), string(status), id)
	if err != nil {
		return fmt.Errorf("update agent status %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("agent %s: %w", id, sql.ErrNoRows)
	}
	return nil
}

// UpdateScopes satisfies the corresponding ports method.
func (r AgentRepository) UpdateScopes(ctx context.Context, id string, scopes []string) error {
	scopesJSON, err := encodeStringList(scopes)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET scopes_json = $1::jsonb WHERE id = $2`, qualify(r.ns, "agents")), scopesJSON, id)
	if err != nil {
		return fmt.Errorf("update agent scopes %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("agent %s: %w", id, sql.ErrNoRows)
	}
	return nil
}

// UpdateAllowedRuns satisfies the corresponding ports method.
func (r AgentRepository) UpdateAllowedRuns(ctx context.Context, id string, runs []string) error {
	runsJSON, err := encodeStringList(runs)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET allowed_runs_json = $1::jsonb WHERE id = $2`, qualify(r.ns, "agents")), runsJSON, id)
	if err != nil {
		return fmt.Errorf("update agent allowed_runs %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("agent %s: %w", id, sql.ErrNoRows)
	}
	return nil
}

func scanAgentRow(row *sql.Row) (domain.Agent, error) {
	var (
		a         domain.Agent
		scopesRaw string
		runsRaw   string
		statusStr string
	)
	if err := row.Scan(&a.ID, &a.Name, &a.OwnerID, &scopesRaw, &runsRaw, &statusStr, &a.CreatedAt); err != nil {
		return domain.Agent{}, err
	}
	a.Status = domain.AgentStatus(statusStr)
	if err := json.Unmarshal([]byte(scopesRaw), &a.Scopes); err != nil {
		return domain.Agent{}, fmt.Errorf("decode agent scopes: %w", err)
	}
	if err := json.Unmarshal([]byte(runsRaw), &a.AllowedRuns); err != nil {
		return domain.Agent{}, fmt.Errorf("decode agent allowed_runs: %w", err)
	}
	return a, nil
}

// Upsert idempotently writes a batch of agents — used by federation.
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
	q := fmt.Sprintf(`
INSERT INTO %s (id, name, owner_id, scopes_json, allowed_runs_json, status, created_at)
VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $7)
ON CONFLICT (id) DO UPDATE SET
	name = EXCLUDED.name,
	owner_id = EXCLUDED.owner_id,
	scopes_json = EXCLUDED.scopes_json,
	allowed_runs_json = EXCLUDED.allowed_runs_json,
	status = EXCLUDED.status`, qualify(r.ns, "agents"))
	for _, a := range agents {
		scopesJSON, err := encodeStringList(a.Scopes)
		if err != nil {
			return err
		}
		runsJSON, err := encodeStringList(a.AllowedRuns)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, q,
			a.ID, a.Name, a.OwnerID, scopesJSON, runsJSON, string(a.Status), a.CreatedAt.UTC(),
		); err != nil {
			return fmt.Errorf("upsert agent %s: %w", a.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("upsert agents: commit: %w", err)
	}
	return nil
}

func scanAgentRows(rows *sql.Rows) (domain.Agent, error) {
	var (
		a         domain.Agent
		scopesRaw string
		runsRaw   string
		statusStr string
	)
	if err := rows.Scan(&a.ID, &a.Name, &a.OwnerID, &scopesRaw, &runsRaw, &statusStr, &a.CreatedAt); err != nil {
		return domain.Agent{}, fmt.Errorf("scan agent row: %w", err)
	}
	a.Status = domain.AgentStatus(statusStr)
	if err := json.Unmarshal([]byte(scopesRaw), &a.Scopes); err != nil {
		return domain.Agent{}, fmt.Errorf("decode agent scopes: %w", err)
	}
	if err := json.Unmarshal([]byte(runsRaw), &a.AllowedRuns); err != nil {
		return domain.Agent{}, fmt.Errorf("decode agent allowed_runs: %w", err)
	}
	return a, nil
}
