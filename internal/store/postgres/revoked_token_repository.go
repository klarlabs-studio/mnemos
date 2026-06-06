package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// RevokedTokenRepository tracks JWTs revoked before their natural
// expiry. Idempotent Add (ON CONFLICT DO NOTHING preserves the
// original revoked_at).
type RevokedTokenRepository struct {
	db *sql.DB
	ns string
}

// Add satisfies the corresponding ports method.
func (r RevokedTokenRepository) Add(ctx context.Context, t domain.RevokedToken) error {
	_, err := r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (jti, revoked_at, expires_at) VALUES ($1, $2, $3)
ON CONFLICT (jti) DO NOTHING`, qualify(r.ns, "revoked_tokens")),
		t.JTI, t.RevokedAt.UTC(), t.ExpiresAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("insert revoked token %s: %w", t.JTI, err)
	}
	return nil
}

// IsRevoked satisfies the corresponding ports method.
func (r RevokedTokenRepository) IsRevoked(ctx context.Context, jti string) (bool, error) {
	var present int
	err := r.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT 1 FROM %s WHERE jti = $1 LIMIT 1`, qualify(r.ns, "revoked_tokens")), jti).Scan(&present)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check revoked token %s: %w", jti, err)
	}
	return true, nil
}

// PurgeExpired satisfies the corresponding ports method.
func (r RevokedTokenRepository) PurgeExpired(ctx context.Context, before time.Time) (int, error) {
	res, err := r.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE expires_at < $1`, qualify(r.ns, "revoked_tokens")), before.UTC())
	if err != nil {
		return 0, fmt.Errorf("purge expired revoked tokens: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
