package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// TestConcurrentUserCreation stresses the user table's UNIQUE(email)
// constraint with N goroutines all trying to create different users
// at once. None should fail and all rows should land — proves the
// busy_timeout PRAGMA is doing its job and that the schema's
// constraint enforcement is per-row, not per-table-lock.
func TestConcurrentUserCreation(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := NewUserRepository(db)

	const N = 20
	var wg sync.WaitGroup
	errs := make(chan error, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			err := repo.Create(context.Background(), domain.User{
				ID:        fmt.Sprintf("usr_concurrent_%d", i),
				Name:      fmt.Sprintf("u%d", i),
				Email:     fmt.Sprintf("u%d@test.local", i),
				Status:    domain.UserStatusActive,
				CreatedAt: time.Now().UTC(),
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent create: %v", e)
	}

	users, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(users) != N {
		t.Errorf("got %d users, want %d", len(users), N)
	}
}

// TestConcurrentDuplicateEmailRejected verifies the UNIQUE
// constraint stays consistent under contention: when two goroutines
// race to insert the same email, exactly one wins, regardless of
// which user id wins.
func TestConcurrentDuplicateEmailRejected(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "dup.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := NewUserRepository(db)

	const N = 10
	var wg sync.WaitGroup
	errs := make(chan error, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			err := repo.Create(context.Background(), domain.User{
				ID:        fmt.Sprintf("usr_dup_%d", i),
				Name:      "dup",
				Email:     "dup@test.local", // every goroutine tries the same email
				Status:    domain.UserStatusActive,
				CreatedAt: time.Now().UTC(),
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	failed := 0
	for range errs {
		failed++
	}
	if failed != N-1 {
		t.Errorf("expected exactly %d failures (one winner), got %d", N-1, failed)
	}

	users, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("got %d users, want 1", len(users))
	}
}

// TestConcurrentRevokedTokenAdd verifies the JWT denylist tolerates
// the same JTI being revoked from two callers at once — the
// repository's ON CONFLICT semantics should make this safe and
// idempotent rather than failing the second writer.
func TestConcurrentRevokedTokenAdd(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "rev.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := NewRevokedTokenRepository(db)

	const N = 10
	const sharedJTI = "jti_concurrent_revoke"
	var wg sync.WaitGroup
	errs := make(chan error, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			err := repo.Add(context.Background(), domain.RevokedToken{
				JTI:       sharedJTI,
				RevokedAt: time.Now().UTC(),
				ExpiresAt: time.Now().Add(time.Hour).UTC(),
			})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent revoke: %v", e)
	}

	revoked, err := repo.IsRevoked(context.Background(), sharedJTI)
	if err != nil {
		t.Fatalf("check revoked: %v", err)
	}
	if !revoked {
		t.Error("token should be on denylist after concurrent Adds")
	}
}
