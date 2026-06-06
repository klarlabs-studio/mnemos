package sqlite

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestRevokedTokenRepository_AddIsRevoked(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	repo := NewRevokedTokenRepository(db)
	ctx := context.Background()

	if got, err := repo.IsRevoked(ctx, "jti_unknown"); err != nil || got {
		t.Fatalf("unknown jti = (%v, %v), want (false, nil)", got, err)
	}

	rt := domain.RevokedToken{
		JTI:       "jti_x",
		RevokedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := repo.Add(ctx, rt); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := repo.IsRevoked(ctx, "jti_x")
	if err != nil || !got {
		t.Fatalf("after add: (%v, %v), want (true, nil)", got, err)
	}
}

func TestRevokedTokenRepository_AddIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	repo := NewRevokedTokenRepository(db)
	ctx := context.Background()

	rt := domain.RevokedToken{JTI: "jti_dup", RevokedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := repo.Add(ctx, rt); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := repo.Add(ctx, rt); err != nil {
		t.Fatalf("second add (should be no-op): %v", err)
	}
}

func TestRevokedTokenRepository_PurgeExpired(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	repo := NewRevokedTokenRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := repo.Add(ctx, domain.RevokedToken{JTI: "old", RevokedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-30 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Add(ctx, domain.RevokedToken{JTI: "new", RevokedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	n, err := repo.PurgeExpired(ctx, now)
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("purged %d, want 1", n)
	}

	gotOld, _ := repo.IsRevoked(ctx, "old")
	gotNew, _ := repo.IsRevoked(ctx, "new")
	if gotOld {
		t.Error("old jti should be purged")
	}
	if !gotNew {
		t.Error("new jti should still be present")
	}
}
