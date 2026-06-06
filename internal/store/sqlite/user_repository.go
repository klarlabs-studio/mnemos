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

// UserRepository persists and retrieves user identities. Backed by
// sqlc-generated queries (see sql/sqlite/query/users.sql).
type UserRepository struct {
	db *sql.DB
	q  *sqlcgen.Queries
}

// NewUserRepository returns a UserRepository backed by the given database.
func NewUserRepository(db *sql.DB) UserRepository {
	return UserRepository{db: db, q: sqlcgen.New(db)}
}

// Create inserts a new user. Returns an error if the email is already
// taken (the schema's UNIQUE constraint on email enforces this).
func (r UserRepository) Create(ctx context.Context, u domain.User) error {
	if err := u.Validate(); err != nil {
		return fmt.Errorf("invalid user: %w", err)
	}
	scopesJSON, err := encodeUserScopes(u.Scopes)
	if err != nil {
		return err
	}
	if err := r.q.CreateUser(ctx, sqlcgen.CreateUserParams{
		ID:         u.ID,
		Name:       u.Name,
		Email:      u.Email,
		Status:     string(u.Status),
		ScopesJson: scopesJSON,
		CreatedAt:  u.CreatedAt.UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return fmt.Errorf("insert user %s: %w", u.ID, err)
	}
	return nil
}

// encodeUserScopes is the inverse of decodeUserScopes — empty slice
// becomes "[]" (not "null") so the column always parses on read.
func encodeUserScopes(scopes []string) (string, error) {
	if scopes == nil {
		scopes = []string{}
	}
	b, err := json.Marshal(scopes)
	if err != nil {
		return "", fmt.Errorf("encode user scopes: %w", err)
	}
	return string(b), nil
}

// UpdateScopes replaces the user's scope list. In-flight tokens keep
// their issued scopes; only freshly-issued tokens see the new list.
func (r UserRepository) UpdateScopes(ctx context.Context, id string, scopes []string) error {
	scopesJSON, err := encodeUserScopes(scopes)
	if err != nil {
		return err
	}
	if err := r.q.UpdateUserScopes(ctx, sqlcgen.UpdateUserScopesParams{
		ScopesJson: scopesJSON,
		ID:         id,
	}); err != nil {
		return fmt.Errorf("update user scopes %s: %w", id, err)
	}
	return r.assertExists(ctx, id)
}

// GetByID returns the user with the given ID, or sql.ErrNoRows.
func (r UserRepository) GetByID(ctx context.Context, id string) (domain.User, error) {
	row, err := r.q.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.User{}, fmt.Errorf("user %s: %w", id, sql.ErrNoRows)
		}
		return domain.User{}, fmt.Errorf("get user %s: %w", id, err)
	}
	return userRowToDomain(row.ID, row.Name, row.Email, row.Status, row.ScopesJson, row.CreatedAt)
}

// GetByEmail returns the user with the given email, or sql.ErrNoRows.
func (r UserRepository) GetByEmail(ctx context.Context, email string) (domain.User, error) {
	row, err := r.q.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.User{}, fmt.Errorf("user %s: %w", email, sql.ErrNoRows)
		}
		return domain.User{}, fmt.Errorf("get user by email %s: %w", email, err)
	}
	return userRowToDomain(row.ID, row.Name, row.Email, row.Status, row.ScopesJson, row.CreatedAt)
}

// List returns all users in created_at order (oldest first). Both
// active and revoked users are returned; callers filter as needed.
func (r UserRepository) List(ctx context.Context) ([]domain.User, error) {
	rows, err := r.q.ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	out := make([]domain.User, 0, len(rows))
	for _, row := range rows {
		u, err := userRowToDomain(row.ID, row.Name, row.Email, row.Status, row.ScopesJson, row.CreatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// UpdateStatus changes a user's status (e.g., active → revoked). Soft
// delete: the row stays so historical created_by references remain
// resolvable.
func (r UserRepository) UpdateStatus(ctx context.Context, id string, status domain.UserStatus) error {
	if err := r.q.UpdateUserStatus(ctx, sqlcgen.UpdateUserStatusParams{
		Status: string(status),
		ID:     id,
	}); err != nil {
		return fmt.Errorf("update user status %s: %w", id, err)
	}
	return r.assertExists(ctx, id)
}

func (r UserRepository) assertExists(ctx context.Context, id string) error {
	if _, err := r.q.GetUserByID(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("user %s: %w", id, sql.ErrNoRows)
		}
		return err
	}
	return nil
}

func userRowToDomain(id, name, email, status, scopesJSON, createdAt string) (domain.User, error) {
	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return domain.User{}, fmt.Errorf("parse user created_at: %w", err)
	}
	u := domain.User{
		ID:        id,
		Name:      name,
		Email:     email,
		Status:    domain.UserStatus(status),
		CreatedAt: t,
	}
	if err := json.Unmarshal([]byte(scopesJSON), &u.Scopes); err != nil {
		return domain.User{}, fmt.Errorf("decode user scopes: %w", err)
	}
	return u, nil
}
