package main

import (
	"context"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

type mcpListClaimsInput struct {
	Type   string `json:"type,omitempty" jsonschema:"description=Filter by claim type: fact, hypothesis, or decision"`
	Status string `json:"status,omitempty" jsonschema:"description=Filter by claim status: active, contested, or deprecated"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=Max number of claims to return (default 50, cap 200)"`
	Offset int    `json:"offset,omitempty" jsonschema:"description=Number of claims to skip"`
}

type mcpListClaimsOutput struct {
	Claims []domain.Claim `json:"claims"`
	Total  int            `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

type mcpListContradictionsInput struct {
	Limit  int `json:"limit,omitempty" jsonschema:"description=Max number of contradictions to return (default 50, cap 200)"`
	Offset int `json:"offset,omitempty" jsonschema:"description=Number of contradictions to skip"`
}

type mcpContradictionPair struct {
	RelationshipID string `json:"relationshipId"`
	FromClaimID    string `json:"fromClaimId"`
	FromClaimText  string `json:"fromClaimText"`
	ToClaimID      string `json:"toClaimId"`
	ToClaimText    string `json:"toClaimText"`
	CreatedAt      string `json:"createdAt"`
}

type mcpListContradictionsOutput struct {
	Contradictions []mcpContradictionPair `json:"contradictions"`
	Total          int                    `json:"total"`
	Limit          int                    `json:"limit"`
	Offset         int                    `json:"offset"`
}

func mcpRunListClaims(ctx context.Context, input mcpListClaimsInput) (mcpListClaimsOutput, error) {
	limit, offset := normalizePagination(input.Limit, input.Offset)

	if input.Type != "" && !validClaimType(input.Type) {
		return mcpListClaimsOutput{}, fmt.Errorf("invalid type %q (want fact, hypothesis, or decision)", input.Type)
	}
	if input.Status != "" && !validClaimStatus(input.Status) {
		return mcpListClaimsOutput{}, fmt.Errorf("invalid status %q (want active, contested, or deprecated)", input.Status)
	}

	conn, err := openConn(ctx)
	if err != nil {
		return mcpListClaimsOutput{}, err
	}
	defer closeConn(conn)

	claims, total, err := listClaimsFiltered(ctx, conn, input.Type, input.Status, limit, offset)
	if err != nil {
		return mcpListClaimsOutput{}, err
	}
	return mcpListClaimsOutput{
		Claims: claims,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	}, nil
}

func mcpRunListContradictions(ctx context.Context, input mcpListContradictionsInput) (mcpListContradictionsOutput, error) {
	limit, offset := normalizePagination(input.Limit, input.Offset)

	conn, err := openConn(ctx)
	if err != nil {
		return mcpListContradictionsOutput{}, err
	}
	defer closeConn(conn)

	pairs, total, err := listContradictionPairs(ctx, conn, limit, offset)
	if err != nil {
		return mcpListContradictionsOutput{}, err
	}
	return mcpListContradictionsOutput{
		Contradictions: pairs,
		Total:          total,
		Limit:          limit,
		Offset:         offset,
	}, nil
}

// listClaimsFiltered loads every claim and filters/paginates in
// memory. The CLI scale (≤ 100k claims) makes a full ListAll cheap
// and keeps the port surface free of bespoke filter parameters.
func listClaimsFiltered(ctx context.Context, conn *store.Conn, claimType, status string, limit, offset int) ([]domain.Claim, int, error) {
	all, err := conn.Claims.ListAll(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list claims: %w", err)
	}
	filtered := make([]domain.Claim, 0, len(all))
	for _, c := range all {
		if claimType != "" && string(c.Type) != claimType {
			continue
		}
		if status != "" && string(c.Status) != status {
			continue
		}
		filtered = append(filtered, c)
	}
	// Sort by created_at descending for the most-recent-first browse
	// experience. ListAll returns ascending; reverse before slicing.
	for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
		filtered[i], filtered[j] = filtered[j], filtered[i]
	}
	total := len(filtered)
	page := paginate(filtered, limit, offset)
	return page, total, nil
}

// listContradictionPairs assembles every contradicts edge with the
// surrounding claim text using ports — Relationships.ListAll +
// Claims.ListByIDs for the hop, then in-memory pagination.
func listContradictionPairs(ctx context.Context, conn *store.Conn, limit, offset int) ([]mcpContradictionPair, int, error) {
	allRels, err := conn.Relationships.ListAll(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list relationships: %w", err)
	}
	contradictions := make([]domain.Relationship, 0)
	for _, r := range allRels {
		if string(r.Type) == "contradicts" {
			contradictions = append(contradictions, r)
		}
	}
	// most-recent-first
	for i, j := 0, len(contradictions)-1; i < j; i, j = i+1, j-1 {
		contradictions[i], contradictions[j] = contradictions[j], contradictions[i]
	}
	total := len(contradictions)
	page := paginate(contradictions, limit, offset)

	// Resolve claim text for the page only — saves materialising
	// every claim row when the operator only asked for one screen.
	claimIDSet := map[string]struct{}{}
	for _, r := range page {
		claimIDSet[r.FromClaimID] = struct{}{}
		claimIDSet[r.ToClaimID] = struct{}{}
	}
	claimIDs := make([]string, 0, len(claimIDSet))
	for id := range claimIDSet {
		claimIDs = append(claimIDs, id)
	}
	textByID := map[string]string{}
	if len(claimIDs) > 0 {
		claims, err := conn.Claims.ListByIDs(ctx, claimIDs)
		if err != nil {
			return nil, 0, fmt.Errorf("resolve claim texts: %w", err)
		}
		for _, c := range claims {
			textByID[c.ID] = c.Text
		}
	}
	pairs := make([]mcpContradictionPair, 0, len(page))
	for _, r := range page {
		pairs = append(pairs, mcpContradictionPair{
			RelationshipID: r.ID,
			FromClaimID:    r.FromClaimID,
			FromClaimText:  textByID[r.FromClaimID],
			ToClaimID:      r.ToClaimID,
			ToClaimText:    textByID[r.ToClaimID],
			CreatedAt:      r.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return pairs, total, nil
}

func normalizePagination(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func validClaimType(s string) bool {
	switch domain.ClaimType(s) {
	case domain.ClaimTypeFact, domain.ClaimTypeHypothesis, domain.ClaimTypeDecision, domain.ClaimTypeTestResult:
		return true
	}
	return false
}

func validClaimStatus(s string) bool {
	switch domain.ClaimStatus(s) {
	case domain.ClaimStatusActive, domain.ClaimStatusContested, domain.ClaimStatusResolved, domain.ClaimStatusDeprecated:
		return true
	}
	return false
}

func validClaimVisibility(s string) bool {
	switch domain.Visibility(s) {
	case domain.VisibilityPersonal, domain.VisibilityTeam, domain.VisibilityOrg:
		return true
	}
	return false
}
