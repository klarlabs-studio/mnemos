package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// UserRepository persists human identities. scopes are stored as
// jsonb; the empty slice serializes as `[]` and is treated by
// callers (see domain.User.Validate / token issuance) as the
// pre-F.3 default of full access.
type UserRepository struct {
	db pgQuerier
	ns string
}

// Create satisfies the corresponding ports method.
func (r UserRepository) Create(ctx context.Context, u domain.User) error {
	if err := u.Validate(); err != nil {
		return fmt.Errorf("invalid user: %w", err)
	}
	scopesJSON, err := encodeStringList(u.Scopes)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (id, name, email, status, scopes_json, created_at)
VALUES ($1, $2, $3, $4, $5::jsonb, $6)`, qualify(r.ns, "users")),
		u.ID, u.Name, u.Email, string(u.Status), scopesJSON, u.CreatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("insert user %s: %w", u.ID, err)
	}
	return nil
}

// GetByID satisfies the corresponding ports method.
func (r UserRepository) GetByID(ctx context.Context, id string) (domain.User, error) {
	return r.scanOne(ctx, `WHERE id = $1`, id)
}

// GetByEmail satisfies the corresponding ports method.
func (r UserRepository) GetByEmail(ctx context.Context, email string) (domain.User, error) {
	return r.scanOne(ctx, `WHERE email = $1`, email)
}

// List satisfies the corresponding ports method.
func (r UserRepository) List(ctx context.Context) ([]domain.User, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, name, email, status, scopes_json::text, created_at FROM %s ORDER BY created_at ASC`, qualify(r.ns, "users")))
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

// UpdateStatus satisfies the corresponding ports method.
func (r UserRepository) UpdateStatus(ctx context.Context, id string, status domain.UserStatus) error {
	res, err := r.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET status = $1 WHERE id = $2`, qualify(r.ns, "users")), string(status), id)
	if err != nil {
		return fmt.Errorf("update user status %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %s: %w", id, sql.ErrNoRows)
	}
	return nil
}

// UpdateScopes satisfies the corresponding ports method.
func (r UserRepository) UpdateScopes(ctx context.Context, id string, scopes []string) error {
	scopesJSON, err := encodeStringList(scopes)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET scopes_json = $1::jsonb WHERE id = $2`, qualify(r.ns, "users")), scopesJSON, id)
	if err != nil {
		return fmt.Errorf("update user scopes %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %s: %w", id, sql.ErrNoRows)
	}
	return nil
}

// scanOne satisfies the corresponding ports method.
func (r UserRepository) scanOne(ctx context.Context, where string, args ...any) (domain.User, error) {
	row := r.db.QueryRowContext(ctx, fmt.Sprintf(`
SELECT id, name, email, status, scopes_json::text, created_at FROM %s `+where, qualify(r.ns, "users")), args...) //nolint:gosec // G202: where is one of two literal constants
	var (
		u         domain.User
		statusStr string
		scopesRaw string
	)
	if err := row.Scan(&u.ID, &u.Name, &u.Email, &statusStr, &scopesRaw, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.User{}, fmt.Errorf("user %v: %w", args, sql.ErrNoRows)
		}
		return domain.User{}, fmt.Errorf("scan user: %w", err)
	}
	u.Status = domain.UserStatus(statusStr)
	if err := json.Unmarshal([]byte(scopesRaw), &u.Scopes); err != nil {
		return domain.User{}, fmt.Errorf("decode user scopes: %w", err)
	}
	return u, nil
}

func scanUserRow(rows *sql.Rows) (domain.User, error) {
	var (
		u         domain.User
		statusStr string
		scopesRaw string
	)
	if err := rows.Scan(&u.ID, &u.Name, &u.Email, &statusStr, &scopesRaw, &u.CreatedAt); err != nil {
		return domain.User{}, fmt.Errorf("scan user row: %w", err)
	}
	u.Status = domain.UserStatus(statusStr)
	if err := json.Unmarshal([]byte(scopesRaw), &u.Scopes); err != nil {
		return domain.User{}, fmt.Errorf("decode user scopes: %w", err)
	}
	return u, nil
}

// encodeStringList is a shared helper for JSON-encoded string lists
// (user/agent scopes, agent allowed_runs). Empty slice serialises
// as "[]" so the column always parses on read.
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
