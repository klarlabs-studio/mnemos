package memory

import (
	"context"
	"database/sql"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// AgentRepository is the in-memory implementation of
// [ports.AgentRepository]. The owner-must-exist constraint enforced
// by SQLite's FK is mirrored here so memory- and SQLite-backed tests
// fail in the same place when an agent references an unknown user.
type AgentRepository struct {
	state *state
}

// Create inserts a new agent. Returns an error when the id is taken
// or the owner does not exist.
func (r AgentRepository) Create(_ context.Context, a domain.Agent) error {
	if err := a.Validate(); err != nil {
		return fmt.Errorf("invalid agent: %w", err)
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if _, exists := r.state.agents[a.ID]; exists {
		return fmt.Errorf("agent %s already exists", a.ID)
	}
	if _, ownerExists := r.state.users[a.OwnerID]; !ownerExists {
		return fmt.Errorf("agent %s: owner user %s does not exist", a.ID, a.OwnerID)
	}
	r.state.agents[a.ID] = storedAgent{
		ID:          a.ID,
		Name:        a.Name,
		OwnerID:     a.OwnerID,
		Scopes:      copyStringSlice(a.Scopes),
		AllowedRuns: copyStringSlice(a.AllowedRuns),
		Status:      a.Status,
		CreatedAt:   a.CreatedAt.UTC(),
	}
	r.state.agentOrder = append(r.state.agentOrder, a.ID)
	return nil
}

// GetByID returns the agent with the given id or an error wrapping
// sql.ErrNoRows when the id is unknown.
func (r AgentRepository) GetByID(_ context.Context, id string) (domain.Agent, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	a, ok := r.state.agents[id]
	if !ok {
		return domain.Agent{}, fmt.Errorf("agent %s: %w", id, sql.ErrNoRows)
	}
	return a.toDomain(), nil
}

// List returns every agent in created_at insertion order.
func (r AgentRepository) List(_ context.Context) ([]domain.Agent, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Agent, 0, len(r.state.agentOrder))
	for _, id := range r.state.agentOrder {
		if a, ok := r.state.agents[id]; ok {
			out = append(out, a.toDomain())
		}
	}
	return out, nil
}

// UpdateStatus changes an agent's status. Returns sql.ErrNoRows when
// the id is unknown.
func (r AgentRepository) UpdateStatus(_ context.Context, id string, status domain.AgentStatus) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	a, ok := r.state.agents[id]
	if !ok {
		return fmt.Errorf("agent %s: %w", id, sql.ErrNoRows)
	}
	a.Status = status
	r.state.agents[id] = a
	return nil
}

// UpdateScopes replaces the agent's scope list. Returns sql.ErrNoRows
// when the id is unknown.
func (r AgentRepository) UpdateScopes(_ context.Context, id string, scopes []string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	a, ok := r.state.agents[id]
	if !ok {
		return fmt.Errorf("agent %s: %w", id, sql.ErrNoRows)
	}
	a.Scopes = copyStringSlice(scopes)
	r.state.agents[id] = a
	return nil
}

// UpdateAllowedRuns replaces the agent's run whitelist. Returns
// sql.ErrNoRows when the id is unknown.
func (r AgentRepository) UpdateAllowedRuns(_ context.Context, id string, runs []string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	a, ok := r.state.agents[id]
	if !ok {
		return fmt.Errorf("agent %s: %w", id, sql.ErrNoRows)
	}
	a.AllowedRuns = copyStringSlice(runs)
	r.state.agents[id] = a
	return nil
}

// Upsert idempotently writes a batch of agents — used by federation.
func (r AgentRepository) Upsert(_ context.Context, agents []domain.Agent) error {
	for _, a := range agents {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("invalid agent %s: %w", a.ID, err)
		}
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	for _, a := range agents {
		r.state.agents[a.ID] = storedAgent{
			ID:          a.ID,
			Name:        a.Name,
			OwnerID:     a.OwnerID,
			Scopes:      copyStringSlice(a.Scopes),
			AllowedRuns: copyStringSlice(a.AllowedRuns),
			Status:      a.Status,
			CreatedAt:   a.CreatedAt,
		}
	}
	return nil
}

func (s storedAgent) toDomain() domain.Agent {
	return domain.Agent{
		ID:          s.ID,
		Name:        s.Name,
		OwnerID:     s.OwnerID,
		Scopes:      copyStringSlice(s.Scopes),
		AllowedRuns: copyStringSlice(s.AllowedRuns),
		Status:      s.Status,
		CreatedAt:   s.CreatedAt,
	}
}
