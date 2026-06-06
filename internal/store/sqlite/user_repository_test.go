package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestUserRepository_CreateThenGet(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	repo := NewUserRepository(db)
	ctx := context.Background()

	u := domain.User{
		ID: "usr_1", Name: "Alice", Email: "alice@example.com",
		Status: domain.UserStatusActive, CreatedAt: time.Now().UTC(),
	}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, "usr_1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "Alice" || got.Email != "alice@example.com" || got.Status != domain.UserStatusActive {
		t.Errorf("got %+v", got)
	}

	byEmail, err := repo.GetByEmail(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if byEmail.ID != "usr_1" {
		t.Errorf("byEmail ID = %q, want usr_1", byEmail.ID)
	}
}

func TestUserRepository_RejectsDuplicateEmail(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	repo := NewUserRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	u1 := domain.User{ID: "usr_a", Name: "A", Email: "x@x.com", Status: domain.UserStatusActive, CreatedAt: now}
	u2 := domain.User{ID: "usr_b", Name: "B", Email: "x@x.com", Status: domain.UserStatusActive, CreatedAt: now}

	if err := repo.Create(ctx, u1); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := repo.Create(ctx, u2); err == nil {
		t.Fatal("expected duplicate email rejection")
	} else if !strings.Contains(err.Error(), "UNIQUE") && !strings.Contains(err.Error(), "constraint") {
		t.Errorf("error doesn't mention uniqueness: %v", err)
	}
}

func TestUserRepository_GetByID_NotFound(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	repo := NewUserRepository(db)

	_, err := repo.GetByID(context.Background(), "usr_nope")
	if err == nil {
		t.Fatal("expected not found error")
	}
}

func TestUserRepository_List_OrderedByCreatedAt(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	repo := NewUserRepository(db)
	ctx := context.Background()

	base := time.Now().UTC()
	for i, name := range []string{"Carol", "Bob", "Alice"} {
		u := domain.User{
			ID: "usr_" + name, Name: name, Email: name + "@x.com",
			Status: domain.UserStatusActive, CreatedAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := repo.Create(ctx, u); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	users, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("len = %d, want 3", len(users))
	}
	if users[0].Name != "Carol" || users[2].Name != "Alice" {
		t.Errorf("ordering wrong: %v", []string{users[0].Name, users[1].Name, users[2].Name})
	}
}

func TestUserRepository_UpdateStatus(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	repo := NewUserRepository(db)
	ctx := context.Background()

	u := domain.User{ID: "usr_r", Name: "R", Email: "r@r.com", Status: domain.UserStatusActive, CreatedAt: time.Now().UTC()}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := repo.UpdateStatus(ctx, "usr_r", domain.UserStatusRevoked); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, _ := repo.GetByID(ctx, "usr_r")
	if got.Status != domain.UserStatusRevoked {
		t.Errorf("status = %q, want revoked", got.Status)
	}

	if err := repo.UpdateStatus(ctx, "usr_nope", domain.UserStatusRevoked); err == nil {
		t.Fatal("expected error for unknown id")
	}
}

func TestUserRepository_RejectsInvalid(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	repo := NewUserRepository(db)

	tests := []domain.User{
		{ID: "", Name: "X", Email: "x@x.com", Status: domain.UserStatusActive, CreatedAt: time.Now()},
		{ID: "usr_x", Name: "", Email: "x@x.com", Status: domain.UserStatusActive, CreatedAt: time.Now()},
		{ID: "usr_x", Name: "X", Email: "", Status: domain.UserStatusActive, CreatedAt: time.Now()},
		{ID: "usr_x", Name: "X", Email: "x@x.com", Status: "weird", CreatedAt: time.Now()},
	}
	for _, u := range tests {
		if err := repo.Create(context.Background(), u); err == nil {
			t.Errorf("Create should reject %+v", u)
		} else if !errors.Is(err, err) { // sanity: error returned
			_ = err
		}
	}
}
