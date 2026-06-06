package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// UserRepository implements ports.UserRepository.
type UserRepository struct {
	db *sql.DB
}

// Create inserts a new user.
func (r UserRepository) Create(ctx context.Context, u domain.User) error {
	if err := u.Validate(); err != nil {
		return fmt.Errorf("invalid user: %w", err)
	}
	scopesJSON, err := encodeStringList(u.Scopes)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO users (id, name, email, status, scopes_json, created_at)
VALUES (?, ?, ?, ?, CAST(? AS JSON), ?)`,
		u.ID, u.Name, u.Email, string(u.Status), scopesJSON, u.CreatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("insert user %s: %w", u.ID, err)
	}
	return nil
}

// GetByID returns the user with the given id.
func (r UserRepository) GetByID(ctx context.Context, id string) (domain.User, error) {
	return r.scanOne(ctx, `WHERE id = ?`, id)
}

// GetByEmail returns the user with the given email.
func (r UserRepository) GetByEmail(ctx context.Context, email string) (domain.User, error) {
	return r.scanOne(ctx, `WHERE email = ?`, email)
}

// List returns every user in created_at insertion order.
func (r UserRepository) List(ctx context.Context) ([]domain.User, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, email, status, scopes_json, created_at FROM users ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]domain.User, 0)
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateStatus changes a user's status.
func (r UserRepository) UpdateStatus(ctx context.Context, id string, status domain.UserStatus) error {
	res, err := r.db.ExecContext(ctx, `UPDATE users SET status = ? WHERE id = ?`, string(status), id)
	if err != nil {
		return fmt.Errorf("update user status %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %s: %w", id, sql.ErrNoRows)
	}
	return nil
}

// UpdateScopes replaces the scope list.
func (r UserRepository) UpdateScopes(ctx context.Context, id string, scopes []string) error {
	scopesJSON, err := encodeStringList(scopes)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `UPDATE users SET scopes_json = CAST(? AS JSON) WHERE id = ?`, scopesJSON, id)
	if err != nil {
		return fmt.Errorf("update user scopes %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %s: %w", id, sql.ErrNoRows)
	}
	return nil
}

func (r UserRepository) scanOne(ctx context.Context, where string, args ...any) (domain.User, error) {
	//nolint:gosec // G202: where is one of two literal constants from internal callers, never user input
	row := r.db.QueryRowContext(ctx, `SELECT id, name, email, status, scopes_json, created_at FROM users `+where, args...)
	var (
		u         domain.User
		statusStr string
		scopesRaw []byte
	)
	if err := row.Scan(&u.ID, &u.Name, &u.Email, &statusStr, &scopesRaw, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.User{}, fmt.Errorf("user %v: %w", args, sql.ErrNoRows)
		}
		return domain.User{}, fmt.Errorf("scan user: %w", err)
	}
	u.Status = domain.UserStatus(statusStr)
	if err := json.Unmarshal(scopesRaw, &u.Scopes); err != nil {
		return domain.User{}, fmt.Errorf("decode user scopes: %w", err)
	}
	return u, nil
}

func scanUserRow(rows *sql.Rows) (domain.User, error) {
	var (
		u         domain.User
		statusStr string
		scopesRaw []byte
	)
	if err := rows.Scan(&u.ID, &u.Name, &u.Email, &statusStr, &scopesRaw, &u.CreatedAt); err != nil {
		return domain.User{}, fmt.Errorf("scan user row: %w", err)
	}
	u.Status = domain.UserStatus(statusStr)
	if err := json.Unmarshal(scopesRaw, &u.Scopes); err != nil {
		return domain.User{}, fmt.Errorf("decode user scopes: %w", err)
	}
	return u, nil
}

func encodeStringList(xs []string) (string, error) {
	if xs == nil {
		xs = []string{}
	}
	b, err := json.Marshal(xs)
	if err != nil {
		return "", fmt.Errorf("encode string list: %w", err)
	}
	return string(b), nil
}
