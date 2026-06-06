package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store/sqlite/sqlcgen"
)

// RevokedTokenRepository tracks JWTs that have been explicitly revoked
// before their natural expiry. Backed by sqlc-generated queries (see
// sql/sqlite/query/revoked_tokens.sql).
type RevokedTokenRepository struct {
	db *sql.DB
	q  *sqlcgen.Queries
}

// NewRevokedTokenRepository returns a RevokedTokenRepository backed by
// the given database.
func NewRevokedTokenRepository(db *sql.DB) RevokedTokenRepository {
	return RevokedTokenRepository{db: db, q: sqlcgen.New(db)}
}

// Add records a token as revoked. Idempotent — re-revoking the same JTI
// is a no-op (preserves the original revoked_at timestamp).
func (r RevokedTokenRepository) Add(ctx context.Context, t domain.RevokedToken) error {
	if err := r.q.AddRevokedToken(ctx, sqlcgen.AddRevokedTokenParams{
		Jti:       t.JTI,
		RevokedAt: t.RevokedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt: t.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return fmt.Errorf("insert revoked token %s: %w", t.JTI, err)
	}
	return nil
}

// IsRevoked returns whether the given JTI is in the denylist.
func (r RevokedTokenRepository) IsRevoked(ctx context.Context, jti string) (bool, error) {
	if _, err := r.q.IsTokenRevoked(ctx, jti); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("check revoked token %s: %w", jti, err)
	}
	return true, nil
}

// PurgeExpired removes denylist entries whose expires_at is before the
// given cutoff. Returns the count removed. Safe to run periodically
// (e.g., on startup) to keep the table bounded.
func (r RevokedTokenRepository) PurgeExpired(ctx context.Context, before time.Time) (int, error) {
	n, err := r.q.PurgeExpiredRevokedTokens(ctx, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("purge expired revoked tokens: %w", err)
	}
	return int(n), nil
}
