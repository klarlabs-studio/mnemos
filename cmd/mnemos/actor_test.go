package main

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestResolveActor_UnsetFallsBackToSystem(t *testing.T) {
	t.Setenv("MNEMOS_USER_ID", "")
	got, err := resolveActor(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != domain.SystemUser {
		t.Errorf("got %q, want %q", got, domain.SystemUser)
	}
}

func TestResolveActor_FlagBeatsEnv(t *testing.T) {
	t.Setenv("MNEMOS_USER_ID", "env_user")
	got, err := resolveActor(context.Background(), nil, "flag_user")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "flag_user" {
		t.Errorf("got %q, want flag_user", got)
	}
}

func TestResolveActor_EnvUsedWhenFlagEmpty(t *testing.T) {
	t.Setenv("MNEMOS_USER_ID", "env_user")
	got, err := resolveActor(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "env_user" {
		t.Errorf("got %q, want env_user", got)
	}
}

func TestResolveActor_ValidatesAgainstDB(t *testing.T) {
	t.Setenv("MNEMOS_USER_ID", "")
	_, conn := openTestStore(t)

	// User exists and is active → resolves to their id.
	userID := "usr_real"
	err := conn.Users.Create(context.Background(), domain.User{
		ID: userID, Name: "Real", Email: "real@test.local",
		Status: domain.UserStatusActive, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := resolveActor(context.Background(), conn.Users, userID)
	if err != nil {
		t.Fatalf("resolve existing: %v", err)
	}
	if got != userID {
		t.Errorf("got %q, want %q", got, userID)
	}

	// Missing user → user error.
	if _, err := resolveActor(context.Background(), conn.Users, "usr_missing"); err == nil {
		t.Error("expected error for missing user, got nil")
	}
}

func TestResolveActor_RevokedUserRejected(t *testing.T) {
	t.Setenv("MNEMOS_USER_ID", "")
	_, conn := openTestStore(t)

	userID := "usr_gone"
	ctx := context.Background()
	err := conn.Users.Create(ctx, domain.User{
		ID: userID, Name: "Gone", Email: "gone@test.local",
		Status: domain.UserStatusActive, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := conn.Users.UpdateStatus(ctx, userID, domain.UserStatusRevoked); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if _, err := resolveActor(ctx, conn.Users, userID); err == nil {
		t.Error("expected error for revoked user, got nil")
	}
}

func TestResolveActor_SystemSentinelPassesThrough(t *testing.T) {
	_, conn := openTestStore(t)

	// The sentinel is never looked up in the users table — it's allowed
	// everywhere as the fallback attribution for unauthenticated writes.
	got, err := resolveActor(context.Background(), conn.Users, domain.SystemUser)
	if err != nil {
		t.Fatalf("resolve system: %v", err)
	}
	if got != domain.SystemUser {
		t.Errorf("got %q, want %q", got, domain.SystemUser)
	}
}

func TestParseFlags_AsCapturesNextArg(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"trailing flag", []string{"ingest", "--as"}, ""},
		{"followed by flag", []string{"ingest", "--as", "--llm", "path"}, ""},
		{"with value", []string{"ingest", "--as", "usr_alice", "path"}, "usr_alice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := ParseFlags(tc.args)
			if f.Actor != tc.want {
				t.Errorf("Actor = %q, want %q", f.Actor, tc.want)
			}
		})
	}
}

func TestStampEventActor_SkipsExisting(t *testing.T) {
	events := []domain.Event{
		{ID: "e1"},
		{ID: "e2", CreatedBy: "usr_preset"},
	}
	stampEventActor(events, "usr_actor")
	if events[0].CreatedBy != "usr_actor" {
		t.Errorf("e1 CreatedBy = %q, want usr_actor", events[0].CreatedBy)
	}
	if events[1].CreatedBy != "usr_preset" {
		t.Errorf("e2 CreatedBy overwritten: %q", events[1].CreatedBy)
	}
}

func TestStampEventActor_EmptyActorIsNoop(t *testing.T) {
	events := []domain.Event{{ID: "e1"}}
	stampEventActor(events, "")
	if events[0].CreatedBy != "" {
		t.Errorf("CreatedBy = %q, want empty (pipeline decides fallback)", events[0].CreatedBy)
	}
}
