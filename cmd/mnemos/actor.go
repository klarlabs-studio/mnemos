package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// resolveActor returns the actor id to stamp on CLI-initiated writes,
// in this order:
//
//  1. --as <id> flag value
//  2. MNEMOS_USER_ID env var
//  3. domain.SystemUser sentinel ("<system>")
//
// The users argument is consulted only when an explicit actor was
// supplied (flag or env): the user must exist and be active. Passing
// nil skips validation — callers that don't have a backend handy
// (e.g. a dry-run path) can use it to just compute the string.
func resolveActor(ctx context.Context, users ports.UserRepository, flagValue string) (string, error) {
	candidate := strings.TrimSpace(flagValue)
	if candidate == "" {
		candidate = strings.TrimSpace(os.Getenv("MNEMOS_USER_ID"))
	}
	if candidate == "" {
		return domain.SystemUser, nil
	}
	if candidate == domain.SystemUser {
		return candidate, nil
	}
	if users == nil {
		return candidate, nil
	}

	user, err := users.GetByID(ctx, candidate)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "not found") {
			return "", NewUserError("user %q not found — create it with 'mnemos user create' first", candidate)
		}
		return "", fmt.Errorf("resolve actor %q: %w", candidate, err)
	}
	if user.Status != domain.UserStatusActive {
		return "", NewUserError("user %q is %s; cannot attribute writes to a non-active user", candidate, user.Status)
	}
	return user.ID, nil
}

// stampEventActor sets CreatedBy on every event that doesn't already
// have one. Used by CLI handlers before persistence so created_by
// carries the flag/env-resolved actor rather than defaulting to
// <system> in the DB.
func stampEventActor(events []domain.Event, actor string) {
	if actor == "" {
		return
	}
	for i := range events {
		if events[i].CreatedBy == "" {
			events[i].CreatedBy = actor
		}
	}
}

// stampClaimActor mirrors stampEventActor for claims.
func stampClaimActor(claims []domain.Claim, actor string) {
	if actor == "" {
		return
	}
	for i := range claims {
		if claims[i].CreatedBy == "" {
			claims[i].CreatedBy = actor
		}
	}
}

// stampRelationshipActor mirrors stampEventActor for relationships.
func stampRelationshipActor(rels []domain.Relationship, actor string) {
	if actor == "" {
		return
	}
	for i := range rels {
		if rels[i].CreatedBy == "" {
			rels[i].CreatedBy = actor
		}
	}
}
