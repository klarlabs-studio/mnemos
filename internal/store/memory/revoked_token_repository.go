package memory

import (
	"context"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// RevokedTokenRepository is the in-memory implementation of
// [ports.RevokedTokenRepository]. Idempotent — re-adding the same
// JTI preserves the original revoked_at, matching SQLite's
// ON CONFLICT(jti) DO NOTHING.
type RevokedTokenRepository struct {
	state *state
}

// Add records a token as revoked. Idempotent.
func (r RevokedTokenRepository) Add(_ context.Context, t domain.RevokedToken) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if _, exists := r.state.revokedTokens[t.JTI]; exists {
		return nil
	}
	r.state.revokedTokens[t.JTI] = storedRevokedToken{
		JTI:       t.JTI,
		RevokedAt: t.RevokedAt.UTC(),
		ExpiresAt: t.ExpiresAt.UTC(),
	}
	return nil
}

// IsRevoked reports whether the given JTI is on the denylist.
func (r RevokedTokenRepository) IsRevoked(_ context.Context, jti string) (bool, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	_, present := r.state.revokedTokens[jti]
	return present, nil
}

// PurgeExpired removes denylist entries whose ExpiresAt is strictly
// before the cutoff. Returns the count removed.
func (r RevokedTokenRepository) PurgeExpired(_ context.Context, before time.Time) (int, error) {
	cutoff := before.UTC()
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	n := 0
	for jti, t := range r.state.revokedTokens {
		if t.ExpiresAt.Before(cutoff) {
			delete(r.state.revokedTokens, jti)
			n++
		}
	}
	return n, nil
}
