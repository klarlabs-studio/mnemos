package memory

import (
	"context"
	"database/sql"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// UserRepository is the in-memory implementation of
// [ports.UserRepository]. Email uniqueness is enforced explicitly to
// match the SQLite UNIQUE(email) column.
type UserRepository struct {
	state *state
}

// Create inserts a new user. Returns an error if the id already
// exists or the email is already taken (matching the SQLite UNIQUE
// constraint surface).
func (r UserRepository) Create(_ context.Context, u domain.User) error {
	if err := u.Validate(); err != nil {
		return fmt.Errorf("invalid user: %w", err)
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if _, exists := r.state.users[u.ID]; exists {
		return fmt.Errorf("user %s already exists", u.ID)
	}
	if _, taken := r.state.usersByEmail[u.Email]; taken {
		return fmt.Errorf("user email %s already taken", u.Email)
	}
	r.state.users[u.ID] = storedUser{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		Status:    u.Status,
		Scopes:    copyStringSlice(u.Scopes),
		CreatedAt: u.CreatedAt.UTC(),
	}
	r.state.usersByEmail[u.Email] = u.ID
	r.state.userOrder = append(r.state.userOrder, u.ID)
	return nil
}

// GetByID returns the user with the given id or an error wrapping
// sql.ErrNoRows when the id is unknown.
func (r UserRepository) GetByID(_ context.Context, id string) (domain.User, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	u, ok := r.state.users[id]
	if !ok {
		return domain.User{}, fmt.Errorf("user %s: %w", id, sql.ErrNoRows)
	}
	return u.toDomain(), nil
}

// GetByEmail returns the user with the given email or an error
// wrapping sql.ErrNoRows when no user matches.
func (r UserRepository) GetByEmail(_ context.Context, email string) (domain.User, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	id, ok := r.state.usersByEmail[email]
	if !ok {
		return domain.User{}, fmt.Errorf("user [%s]: %w", email, sql.ErrNoRows)
	}
	return r.state.users[id].toDomain(), nil
}

// List returns every user in created_at insertion order.
func (r UserRepository) List(_ context.Context) ([]domain.User, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.User, 0, len(r.state.userOrder))
	for _, id := range r.state.userOrder {
		if u, ok := r.state.users[id]; ok {
			out = append(out, u.toDomain())
		}
	}
	return out, nil
}

// UpdateStatus changes a user's status. Returns sql.ErrNoRows when
// the id is unknown.
func (r UserRepository) UpdateStatus(_ context.Context, id string, status domain.UserStatus) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	u, ok := r.state.users[id]
	if !ok {
		return fmt.Errorf("user %s: %w", id, sql.ErrNoRows)
	}
	u.Status = status
	r.state.users[id] = u
	return nil
}

// UpdateScopes replaces the user's scope list. Returns sql.ErrNoRows
// when the id is unknown.
func (r UserRepository) UpdateScopes(_ context.Context, id string, scopes []string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	u, ok := r.state.users[id]
	if !ok {
		return fmt.Errorf("user %s: %w", id, sql.ErrNoRows)
	}
	u.Scopes = copyStringSlice(scopes)
	r.state.users[id] = u
	return nil
}

func (s storedUser) toDomain() domain.User {
	return domain.User{
		ID:        s.ID,
		Name:      s.Name,
		Email:     s.Email,
		Status:    s.Status,
		Scopes:    copyStringSlice(s.Scopes),
		CreatedAt: s.CreatedAt,
	}
}
