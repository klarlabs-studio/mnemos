package pipeline

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// Id-addressed writes (ADR 0041) are the bulk writes whose claims, evidence,
// associations and embeddings name claims by the SENDER's ids: REST
// POST /v1/beliefs, /v1/associations and /v1/embeddings, the gRPC Append*
// methods, and registry push and pull. They cannot go through
// CollapseKnownClaims as an ingest batch does, because the rows that arrive
// after the beliefs (associations, embeddings) still carry the sender's ids.
//
// Two rules make them agree with the merge redirects of ADR 0033:
//
//  1. A write addressed to a redirect follows the redirect. The redirect row is
//     never rewritten, so it cannot come back active, and what the write
//     carries (evidence, association ends) lands on the live claim at the end
//     of the merged_into chain. An embedding for a redirect is dropped: the
//     live claim has its own.
//  2. An unknown id whose belief a live claim already holds — same
//     domain.ClaimTextKey, scope and audience — is stored as a redirect to
//     that claim, and its evidence moves there. Brains mint random claim ids,
//     so this is how the same belief pushed from two brains stays one belief.
//     Storing the redirect (rather than discarding the id) is what lets the
//     associations and embeddings that follow resolve through rule 1, and it
//     keeps the id from ever being reused.
//
// A stored claim that is not a redirect is always an update of that claim,
// never folded: an explicit write to a known id is an edit.

// AddressedBeliefs is an id-addressed claim batch resolved against the store.
type AddressedBeliefs struct {
	// Claims are the claims to upsert as sent: updates of stored non-redirect
	// claims and new claims no live claim holds.
	Claims []domain.Claim
	// Redirects are new claims stored as redirects instead, keyed by the id
	// of the claim that holds their belief. Each is deprecated and carries
	// MergedInto.
	Redirects map[string][]domain.Claim
	// Links are the evidence links with every claim id resolved. A link moved
	// onto another claim keeps at most one link per (claim, origin), as a
	// restatement does (#443).
	Links []domain.ClaimEvidence
	// Remap maps each incoming id that resolved elsewhere — a stored redirect
	// or a folded restatement — to the live claim it resolved to.
	Remap map[string]string
}

// Folded counts the incoming claims stored as new redirects.
func (a AddressedBeliefs) Folded() int {
	n := 0
	for _, rs := range a.Redirects {
		n += len(rs)
	}
	return n
}

// ResolveAddressedBeliefs applies both rules to an id-addressed claim batch
// and its evidence links. Nothing is written.
func ResolveAddressedBeliefs(ctx context.Context, conn *store.Conn, claims []domain.Claim, links []domain.ClaimEvidence) (AddressedBeliefs, error) {
	ids := make([]string, 0, len(claims)+len(links))
	for _, c := range claims {
		ids = append(ids, c.ID)
	}
	for _, l := range links {
		ids = append(ids, l.ClaimID)
	}
	stored, err := storedClaims(ctx, conn, ids)
	if err != nil {
		return AddressedBeliefs{}, err
	}
	remap, err := redirectTargets(ctx, conn, stored)
	if err != nil {
		return AddressedBeliefs{}, err
	}
	out := AddressedBeliefs{Redirects: map[string][]domain.Claim{}, Remap: remap}

	var candidates []domain.Claim
	for _, c := range claims {
		if _, ok := stored[c.ID]; !ok && foldable(c) {
			candidates = append(candidates, c)
		}
	}
	owner, err := heldBeliefs(ctx, conn, candidates)
	if err != nil {
		return AddressedBeliefs{}, err
	}
	// A stored claim this batch updates holds its belief like any live claim;
	// one this batch retires holds nothing, so nothing is folded onto it.
	retiring := map[string]struct{}{}
	for _, c := range claims {
		if s, known := stored[c.ID]; known && s.MergedInto == "" {
			if foldable(c) {
				claimOwner(owner, c)
			} else {
				retiring[c.ID] = struct{}{}
			}
		}
	}
	for _, c := range claims {
		s, known := stored[c.ID]
		switch {
		case known && s.MergedInto != "":
			// Rule 1: never rewrite a redirect.
			continue
		case known || !foldable(c):
			out.Claims = append(out.Claims, c)
			continue
		}
		to, ok := owner[identityOf(c)]
		if _, retired := retiring[to]; ok && to != c.ID && !retired {
			r := c
			r.Status = domain.ClaimStatusDeprecated
			r.MergedInto = to
			out.Redirects[to] = append(out.Redirects[to], r)
			out.Remap[c.ID] = to
			continue
		}
		owner[identityOf(c)] = c.ID
		out.Claims = append(out.Claims, c)
	}

	out.Links, err = resolveAddressedLinks(ctx, conn, links, out.Remap)
	if err != nil {
		return AddressedBeliefs{}, err
	}
	return out, nil
}

// foldable reports whether an incoming claim may be folded onto a held belief:
// it must state something and be live itself. A retired claim is not a
// restatement of a live belief, and folding it would hand its evidence to a
// belief its sender had already retired.
func foldable(c domain.Claim) bool {
	return c.ID != "" && c.Status != domain.ClaimStatusDeprecated && c.ValidTo.IsZero() &&
		domain.NormalizeClaimText(c.Text) != ""
}

// claimOwner records c as holding its belief unless a claim already does.
func claimOwner(owner map[beliefIdentity]string, c domain.Claim) {
	if _, ok := owner[identityOf(c)]; !ok {
		owner[identityOf(c)] = c.ID
	}
}

// storedClaims reads the stored claims among ids, keyed by id.
func storedClaims(ctx context.Context, conn *store.Conn, ids []string) (map[string]domain.Claim, error) {
	seen := make(map[string]struct{}, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup || id == "" {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	out := make(map[string]domain.Claim, len(unique))
	if len(unique) == 0 {
		return out, nil
	}
	found, err := conn.Claims.ListByIDs(ctx, unique)
	if err != nil {
		return nil, fmt.Errorf("look up addressed claims: %w", err)
	}
	for _, c := range found {
		out[c.ID] = c
	}
	return out, nil
}

// redirectTargets maps each stored redirect to the live claim its chain ends
// at. A redirect whose chain ends at a claim that is gone or no longer live
// has no entry: what is addressed to it stays on it, and it is still never
// rewritten (ADR 0041).
func redirectTargets(ctx context.Context, conn *store.Conn, stored map[string]domain.Claim) (map[string]string, error) {
	out := map[string]string{}
	for id, c := range stored {
		if c.MergedInto == "" {
			continue
		}
		target, err := resolveRedirect(ctx, conn, c)
		if err != nil {
			return nil, err
		}
		if target != "" && target != id {
			out[id] = target
		}
	}
	return out, nil
}

// resolveAddressedLinks points every link at its resolved claim. Links that
// moved keep at most one link per (claim, origin), counting the evidence the
// claim they moved to already has; links addressed straight at a live claim
// are kept as sent.
func resolveAddressedLinks(ctx context.Context, conn *store.Conn, links []domain.ClaimEvidence, remap map[string]string) ([]domain.ClaimEvidence, error) {
	if len(remap) == 0 {
		return links, nil
	}
	var direct, moved []domain.ClaimEvidence
	for _, l := range links {
		if to, ok := remap[l.ClaimID]; ok {
			l.ClaimID = to
			moved = append(moved, l)
			continue
		}
		direct = append(direct, l)
	}
	direct = rewriteLinks(direct, nil)
	moved, err := dropRepeatedOrigins(ctx, conn, nil, rewriteLinks(moved, nil))
	if err != nil {
		return nil, err
	}
	seen := make(map[[2]string]struct{}, len(direct))
	for _, l := range direct {
		seen[l.Key()] = struct{}{}
	}
	out := direct
	for _, l := range moved {
		if _, dup := seen[l.Key()]; !dup {
			out = append(out, l)
		}
	}
	return out, nil
}

// StoredRedirects maps each id among ids that is stored as a merge redirect to
// the live claim its chain ends at. Ids that are not redirects, or whose chain
// ends at nothing live, have no entry.
func StoredRedirects(ctx context.Context, conn *store.Conn, ids []string) (map[string]string, error) {
	stored, err := storedClaims(ctx, conn, ids)
	if err != nil {
		return nil, err
	}
	return redirectTargets(ctx, conn, stored)
}

// IsStoredRedirect reports whether claimID is stored as a merge redirect,
// whatever its chain resolves to.
func IsStoredRedirect(ctx context.Context, conn *store.Conn, claimID string) (bool, error) {
	stored, err := storedClaims(ctx, conn, []string{claimID})
	if err != nil {
		return false, err
	}
	return stored[claimID].MergedInto != "", nil
}

// FollowRedirectsInRelationships points every association end that is a merge
// redirect at the live claim its chain ends at, and drops the edges the rewrite
// turns into self-loops or repeats (ADR 0041).
func FollowRedirectsInRelationships(ctx context.Context, conn *store.Conn, rels []domain.Relationship) ([]domain.Relationship, error) {
	if len(rels) == 0 {
		return rels, nil
	}
	ids := make([]string, 0, 2*len(rels))
	for _, r := range rels {
		ids = append(ids, r.FromClaimID, r.ToClaimID)
	}
	remap, err := StoredRedirects(ctx, conn, ids)
	if err != nil {
		return nil, err
	}
	return remapRelationships(rels, remap), nil
}
