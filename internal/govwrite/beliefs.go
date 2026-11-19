package govwrite

import (
	"context"
	"fmt"
	"sort"

	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/store"
)

// --- Id-addressed beliefs (ADR 0041) ---

// BeliefsResult is what an id-addressed belief write did.
type BeliefsResult struct {
	// Accepted is the number of claims the write took, including those that
	// resolved to another claim.
	Accepted int
	// Folded is the number of new ids stored as redirects because a live
	// claim already held their belief.
	Folded int
	// Remapped maps each incoming id that resolved elsewhere — a stored merge
	// redirect or a folded restatement — to the live claim its writes landed
	// on. Associations and embeddings sent for an incoming id later resolve
	// the same way.
	Remapped map[string]string
}

type beliefsInput struct {
	Claims []domain.Claim
	Links  []domain.ClaimEvidence
	Reason ClaimReason
}

type beliefsExecutor struct{ conn *store.Conn }

// Execute resolves the batch against the store's merge redirects and held
// beliefs, then upserts the claims, the new redirects and the evidence.
func (e beliefsExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := payload[beliefsInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("write_beliefs: unexpected input %T", input)
	}
	res, err := pipeline.ResolveAddressedBeliefs(ctx, e.conn, in.Claims, in.Links)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, err
	}
	if len(res.Claims) > 0 {
		if err := upsertClaims(ctx, e.conn, res.Claims, in.Reason); err != nil {
			return axidomain.ExecutionResult{}, nil, fmt.Errorf("upsert claims: %w", err)
		}
	}
	if err := e.storeRedirects(ctx, res.Redirects, in.Reason); err != nil {
		return axidomain.ExecutionResult{}, nil, err
	}
	if len(res.Links) > 0 {
		if err := e.conn.Claims.UpsertEvidence(ctx, res.Links); err != nil {
			return axidomain.ExecutionResult{}, nil, fmt.Errorf("upsert evidence links: %w", err)
		}
	}
	out := BeliefsResult{Accepted: len(in.Claims), Folded: res.Folded(), Remapped: res.Remap}
	return axidomain.ExecutionResult{
		Data:    out,
		Summary: fmt.Sprintf("wrote %d belief(s), %d folded onto held beliefs, %d remapped", out.Accepted, out.Folded, len(out.Remapped)),
	}, ev("mnemos.write.beliefs", map[string]any{
		"count": out.Accepted, "upserted": len(res.Claims), "folded": out.Folded,
		"remapped": out.Remapped, "evidence_links": len(res.Links),
		"reason": in.Reason.Reason, "changed_by": in.Reason.ChangedBy,
	}), nil
}

// storeRedirects writes each folded claim as a redirect, one status-history
// reason per claim it now points at.
func (e beliefsExecutor) storeRedirects(ctx context.Context, redirects map[string][]domain.Claim, reason ClaimReason) error {
	targets := make([]string, 0, len(redirects))
	for to := range redirects {
		targets = append(targets, to)
	}
	sort.Strings(targets)
	for _, to := range targets {
		why := "restatement folded onto " + to
		if reason.Reason != "" {
			why = reason.Reason + ": " + why
		}
		changedBy := reason.ChangedBy
		if changedBy == "" {
			changedBy = domain.SystemUser
		}
		if err := e.conn.Claims.UpsertWithReasonAs(ctx, redirects[to], why, changedBy); err != nil {
			return fmt.Errorf("store redirects to %s: %w", to, err)
		}
	}
	return nil
}

// Beliefs writes an id-addressed belief batch with its evidence links: the
// write REST POST /v1/beliefs, gRPC AppendBeliefs and registry pull make with
// the sender's claim ids. Unlike [Writer.Claims] it never rewrites a merge
// redirect (its writes follow the redirect to the live claim), and it stores a
// new id whose belief a live claim already holds as a redirect to that claim
// (ADR 0041).
func (w *Writer) Beliefs(ctx context.Context, claims []domain.Claim, links []domain.ClaimEvidence, reason ClaimReason) (BeliefsResult, error) {
	return dispatch[BeliefsResult](ctx, w, actionWriteBeliefs, beliefsInput{Claims: claims, Links: links, Reason: reason})
}

// upsertClaims routes a claim upsert through the repository method that
// records the reason the write carries.
func upsertClaims(ctx context.Context, conn *store.Conn, claims []domain.Claim, reason ClaimReason) error {
	switch {
	case reason.AssertStatus:
		if reason.Reason == "" {
			return fmt.Errorf("asserting a status requires a reason")
		}
		return conn.Claims.UpsertAssertingStatusAs(ctx, claims, reason.Reason, reason.ChangedBy)
	case reason.ChangedBy != "":
		return conn.Claims.UpsertWithReasonAs(ctx, claims, reason.Reason, reason.ChangedBy)
	case reason.Reason != "":
		return conn.Claims.UpsertWithReason(ctx, claims, reason.Reason)
	default:
		return conn.Claims.Upsert(ctx, claims)
	}
}
