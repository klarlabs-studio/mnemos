// Package beliefops holds the belief operations every transport exposes:
// governance edits (deprecate, update, resolve a dissonance, escalate,
// verify), feedback, provenance and history reads, and entity-association
// browse and append.
//
// Each operation is implemented ONCE here. The MCP tools, the REST routes,
// the gRPC methods and the root library are thin adapters over it: they
// translate their own request into the arguments below, call the function,
// and map its sentinel errors onto their own status codes. Before this
// package each transport carried its own copy, and they had drifted (MCP
// feedback never decayed confidence while REST did).
//
// Reads take the *store.Conn the caller already scoped (tenant, brain).
// Writes go through a [Writer]: the daemon passes its governed
// *govwrite.Writer, the library passes govwrite.Direct from inside its own
// governed kernel. Nothing here writes to a repository directly.
package beliefops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/store"
)

// Sentinel errors every transport maps onto its own status codes.
var (
	// ErrNotFound: a named belief does not exist (REST 404, gRPC NotFound).
	ErrNotFound = errors.New("belief not found")
	// ErrInvalid: the request is malformed (REST 400, gRPC InvalidArgument).
	ErrInvalid = errors.New("invalid request")
	// ErrUnsupported: the backend lacks the storage this operation needs
	// (REST 501, gRPC Unimplemented).
	ErrUnsupported = errors.New("not supported by this backend")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Writer is the governed write surface the operations need. *govwrite.Writer
// and *govwrite.DirectWriter both satisfy it.
type Writer interface {
	Claims(ctx context.Context, claims []domain.Claim, reason govwrite.ClaimReason) (int, error)
	MarkVerified(ctx context.Context, claimID string, verifiedAt time.Time, halfLifeDays float64) error
	Feedback(ctx context.Context, state domain.ClaimFeedback) (string, error)
}

// Default audit reasons, recorded on the status transition when the caller
// gives none. They are the reasons the MCP tools have always written.
const (
	ReasonDeprecate = "agent-deprecated"
	ReasonForget    = "agent-forget"
	ReasonUpdate    = "agent-update"
	ReasonResolve   = "agent-resolved-contradiction"
)

// Require reports ErrInvalid for a blank id and ErrNotFound for an unknown
// one. Callers that run an operation inside a kernel which flattens errors to
// text (the root library) call it first, so the sentinel survives.
func Require(ctx context.Context, conn *store.Conn, ids ...string) error {
	trimmed := make([]string, len(ids))
	for i, id := range ids {
		trimmed[i] = strings.TrimSpace(id)
	}
	_, err := loadClaims(ctx, conn, trimmed...)
	return err
}

// loadClaims returns the named claims in request order, or ErrNotFound when
// any is missing.
func loadClaims(ctx context.Context, conn *store.Conn, ids ...string) ([]domain.Claim, error) {
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, invalid("belief id is required")
		}
	}
	found, err := conn.Claims.ListByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("look up belief: %w", err)
	}
	byID := make(map[string]domain.Claim, len(found))
	for _, c := range found {
		byID[c.ID] = c
	}
	out := make([]domain.Claim, 0, len(ids))
	for _, id := range ids {
		c, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		out = append(out, c)
	}
	return out, nil
}

func reasonOr(reason, fallback string) string {
	if r := strings.TrimSpace(reason); r != "" {
		return r
	}
	return fallback
}
