package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// RevokedTokenRepository implements ports.RevokedTokenRepository.
type RevokedTokenRepository struct {
	db *sql.DB
}

// Add records a token as revoked. Idempotent via INSERT IGNORE.
func (r RevokedTokenRepository) Add(ctx context.Context, t domain.RevokedToken) error {
	_, err := r.db.ExecContext(ctx, `
INSERT IGNORE INTO revoked_tokens (jti, revoked_at, expires_at) VALUES (?, ?, ?)`,
		t.JTI, t.RevokedAt.UTC(), t.ExpiresAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("insert revoked token %s: %w", t.JTI, err)
	}
	return nil
}

// IsRevoked reports denylist membership.
func (r RevokedTokenRepository) IsRevoked(ctx context.Context, jti string) (bool, error) {
	var present int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM revoked_tokens WHERE jti = ? LIMIT 1`, jti).Scan(&present)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check revoked token %s: %w", jti, err)
	}
	return true, nil
}

// PurgeExpired removes denylist entries past their expires_at.
func (r RevokedTokenRepository) PurgeExpired(ctx context.Context, before time.Time) (int, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM revoked_tokens WHERE expires_at < ?`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("purge expired revoked tokens: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
