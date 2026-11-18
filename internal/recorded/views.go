package recorded

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/trust"
)

// Events hides events ingested after At. Only the list reads are bounded.
type Events struct {
	ports.EventRepository
	At time.Time
}

func (r Events) keep(events []domain.Event, err error) ([]domain.Event, error) {
	if err != nil {
		return nil, err
	}
	out := events[:0:0]
	for _, ev := range events {
		if !ev.IngestedAt.After(r.At) {
			out = append(out, ev)
		}
	}
	return out, nil
}

// ListAll returns the events ingested at or before At.
func (r Events) ListAll(ctx context.Context) ([]domain.Event, error) {
	return r.keep(r.EventRepository.ListAll(ctx))
}

// ListByIDs returns those of ids ingested at or before At.
func (r Events) ListByIDs(ctx context.Context, ids []string) ([]domain.Event, error) {
	return r.keep(r.EventRepository.ListByIDs(ctx, ids))
}

// ListByRunID returns the run's events ingested at or before At.
func (r Events) ListByRunID(ctx context.Context, runID string) ([]domain.Event, error) {
	return r.keep(r.EventRepository.ListByRunID(ctx, runID))
}

// Claims returns claims rewound to At. Only reads are bounded; the time-travel
// paths never write through it.
//
// Merges (ADR 0036 step 2). Semantic dedupe repoints a loser's evidence onto
// the winner and keeps the loser as a redirect. The view walks that backwards:
//
//   - ListByEventIDs reaches the claim that holds the evidence now and adds
//     every claim merged into it (transitively) that the brain still held
//     separately at At, i.e. merged after At. Evidence links were moved, not
//     versioned, so such a claim is reached through all of its winner's
//     evidence, not only its own: an over-approximation between claims that
//     were near-duplicates, which the merge required.
//   - ListEvidenceByClaimIDs attributes a claim held separately at At the
//     evidence of the claim that absorbed it, for the same reason.
//   - Evidence on events ingested after At is dropped when Events is set.
type Claims struct {
	ports.ClaimRepository
	// Events, when set, bounds evidence links to events ingested by At.
	Events ports.EventRepository
	At     time.Time
}

// maxMergeDepth bounds a redirect walk. Chains are short (a merge needs a
// live target); the bound only guards a corrupt cycle the seen-set misses.
const maxMergeDepth = 64

// history is one claim's versioned past.
type history struct {
	status   []domain.ClaimStatusTransition
	validity []domain.ValidityChange
}

func (r Claims) historyOf(ctx context.Context, id string) (history, error) {
	status, err := r.ClaimRepository.ListStatusHistoryByClaimID(ctx, id)
	if err != nil {
		return history{}, fmt.Errorf("status history of %s: %w", id, err)
	}
	validity, err := r.ClaimRepository.ListValidityHistory(ctx, id)
	if err != nil {
		return history{}, fmt.Errorf("validity history of %s: %w", id, err)
	}
	return history{status: status, validity: validity}, nil
}

// rewind returns the claims recorded by At, each as it was then.
func (r Claims) rewind(ctx context.Context, claims []domain.Claim) ([]domain.Claim, error) {
	out := make([]domain.Claim, 0, len(claims))
	for _, c := range claims {
		if c.CreatedAt.After(r.At) {
			continue
		}
		h, err := r.historyOf(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		if rc, ok := Rewind(c, h.status, h.validity, r.At); ok {
			out = append(out, rc)
		}
	}
	return r.scoreAt(ctx, out)
}

// scoreAt replaces each claim's stored trust_score, today's, with its trust at
// At (TrustAt). It reads the claims' evidence through the view, so merges and
// the event bound apply, the episodes those links name, and the credit
// history. Without Events the view cannot date evidence and the stored score
// stands; both callers (recall, browse) set it.
func (r Claims) scoreAt(ctx context.Context, claims []domain.Claim) ([]domain.Claim, error) {
	if r.Events == nil || len(claims) == 0 {
		return claims, nil
	}
	ids := make([]string, len(claims))
	for i, c := range claims {
		ids[i] = c.ID
	}
	links, err := r.ListEvidenceByClaimIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("evidence for trust at %s: %w", r.At.Format(time.RFC3339), err)
	}
	eventIDs := make([]string, 0, len(links))
	byClaim := make(map[string][]domain.ClaimEvidence, len(claims))
	for _, l := range links {
		eventIDs = append(eventIDs, l.EventID)
		byClaim[l.ClaimID] = append(byClaim[l.ClaimID], l)
	}
	events := map[string]domain.Event{}
	if len(eventIDs) > 0 {
		evs, err := r.Events.ListByIDs(ctx, eventIDs)
		if err != nil {
			return nil, fmt.Errorf("evidence events for trust: %w", err)
		}
		for _, ev := range evs {
			events[ev.ID] = ev
		}
	}
	var credit []domain.CreditChange
	if lister, ok := r.ClaimRepository.(ports.CreditHistoryLister); ok {
		if credit, err = lister.ListCreditHistory(ctx, ids); err != nil {
			return nil, fmt.Errorf("credit history: %w", err)
		}
	}
	creditOf := make(map[string][]domain.CreditChange)
	for _, ch := range credit {
		creditOf[ch.ClaimID] = append(creditOf[ch.ClaimID], ch)
	}
	for i := range claims {
		c := &claims[i]
		c.TrustScore = TrustAt(*c, byClaim[c.ID], events, creditOf[c.ID], r.At)
		c.TrustComputedAt = r.At
		c.TrustModelVersion = trust.ModelVersion
	}
	return claims, nil
}

// mergedAway returns every claim merged into one of claims, transitively,
// excluding claims already given.
func (r Claims) mergedAway(ctx context.Context, claims []domain.Claim) ([]domain.Claim, error) {
	seen := make(map[string]bool, len(claims))
	frontier := make([]string, 0, len(claims))
	for _, c := range claims {
		if !seen[c.ID] {
			seen[c.ID] = true
			frontier = append(frontier, c.ID)
		}
	}
	var out []domain.Claim
	for depth := 0; len(frontier) > 0 && depth < maxMergeDepth; depth++ {
		redirects, err := r.ListMergedInto(ctx, frontier)
		if err != nil {
			return nil, fmt.Errorf("list merge redirects: %w", err)
		}
		frontier = frontier[:0]
		for _, c := range redirects {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			out = append(out, c)
			frontier = append(frontier, c.ID)
		}
	}
	return out, nil
}

// ListAll returns every claim recorded by At, as it was then. A claim merged
// after At reads as the separate belief it still was. Histories are read in
// two bulk calls, not two per claim, so a whole-store browse stays three reads.
func (r Claims) ListAll(ctx context.Context) ([]domain.Claim, error) {
	claims, err := r.ClaimRepository.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	status, err := r.ListAllStatusHistory(ctx)
	if err != nil {
		return nil, fmt.Errorf("status history: %w", err)
	}
	validity, err := r.ClaimRepository.ListValidityHistory(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("validity history: %w", err)
	}
	byClaim := make(map[string]history)
	for _, s := range status {
		h := byClaim[s.ClaimID]
		h.status = append(h.status, s)
		byClaim[s.ClaimID] = h
	}
	for _, v := range validity {
		h := byClaim[v.ClaimID]
		h.validity = append(h.validity, v)
		byClaim[v.ClaimID] = h
	}
	out := make([]domain.Claim, 0, len(claims))
	for _, c := range claims {
		h := byClaim[c.ID]
		sort.SliceStable(h.status, func(i, j int) bool { return h.status[i].ChangedAt.Before(h.status[j].ChangedAt) })
		sort.SliceStable(h.validity, func(i, j int) bool { return h.validity[i].ChangedAt.Before(h.validity[j].ChangedAt) })
		if rc, ok := Rewind(c, h.status, h.validity, r.At); ok {
			out = append(out, rc)
		}
	}
	return r.scoreAt(ctx, out)
}

// ListByIDs returns those of ids recorded by At, as they were then.
func (r Claims) ListByIDs(ctx context.Context, ids []string) ([]domain.Claim, error) {
	claims, err := r.ClaimRepository.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	return r.rewind(ctx, claims)
}

// ListByEventIDs returns the claims the events supported at At: the claims
// holding their evidence now, plus every claim merged into those after At,
// which held its own evidence then.
func (r Claims) ListByEventIDs(ctx context.Context, eventIDs []string) ([]domain.Claim, error) {
	holders, err := r.ClaimRepository.ListByEventIDs(ctx, eventIDs)
	if err != nil {
		return nil, err
	}
	out, err := r.rewind(ctx, holders)
	if err != nil {
		return nil, err
	}
	merged, err := r.mergedAway(ctx, holders)
	if err != nil {
		return nil, err
	}
	rewound, err := r.rewind(ctx, merged)
	if err != nil {
		return nil, err
	}
	for _, c := range rewound {
		// A claim already a redirect at At held no evidence then either.
		if c.MergedInto == "" {
			out = append(out, c)
		}
	}
	return out, nil
}

// heldRoots maps each of ids that was still held separately at At but has been
// merged since to the claim that holds its evidence now (the end of its
// redirect chain).
func (r Claims) heldRoots(ctx context.Context, ids []string) (map[string]string, error) {
	claims, err := r.ClaimRepository.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	roots := map[string]string{}
	for _, c := range claims {
		if c.MergedInto == "" || c.CreatedAt.After(r.At) {
			continue
		}
		h, err := r.historyOf(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		if at, ok := MergedAt(h.status); !ok || !at.After(r.At) {
			continue
		}
		root, err := r.chainEnd(ctx, c.MergedInto)
		if err != nil {
			return nil, err
		}
		roots[c.ID] = root
	}
	return roots, nil
}

// chainEnd follows merge redirects from id to the claim that is not one.
func (r Claims) chainEnd(ctx context.Context, id string) (string, error) {
	seen := map[string]bool{}
	for depth := 0; depth < maxMergeDepth && !seen[id]; depth++ {
		seen[id] = true
		next, err := r.ClaimRepository.ListByIDs(ctx, []string{id})
		if err != nil {
			return "", err
		}
		if len(next) == 0 || next[0].MergedInto == "" {
			return id, nil
		}
		id = next[0].MergedInto
	}
	return id, nil
}

// ListEvidenceByClaimIDs returns the evidence links of ids as they stood at At.
// A claim merged after At is attributed the evidence of the claim that holds
// it now (see Claims).
func (r Claims) ListEvidenceByClaimIDs(ctx context.Context, ids []string) ([]domain.ClaimEvidence, error) {
	roots, err := r.heldRoots(ctx, ids)
	if err != nil {
		return nil, err
	}
	query := append([]string(nil), ids...)
	heldBy := map[string][]string{} // root -> merged claims it stands in for
	for id, root := range roots {
		query = append(query, root)
		heldBy[root] = append(heldBy[root], id)
	}
	links, err := r.ClaimRepository.ListEvidenceByClaimIDs(ctx, query)
	if err != nil {
		return nil, err
	}
	asked := make(map[string]bool, len(ids))
	for _, id := range ids {
		asked[id] = true
	}
	seen := map[[2]string]bool{}
	var out []domain.ClaimEvidence
	add := func(l domain.ClaimEvidence) {
		if !seen[l.Key()] {
			seen[l.Key()] = true
			out = append(out, l)
		}
	}
	for _, l := range links {
		if asked[l.ClaimID] {
			add(l)
		}
		for _, merged := range heldBy[l.ClaimID] {
			add(domain.ClaimEvidence{ClaimID: merged, EventID: l.EventID, CreatedAt: l.CreatedAt})
		}
	}
	return r.recordedEvidence(ctx, out)
}

// recordedEvidence drops links to events ingested after At, when Events is set.
func (r Claims) recordedEvidence(ctx context.Context, links []domain.ClaimEvidence) ([]domain.ClaimEvidence, error) {
	if r.Events == nil || len(links) == 0 {
		return links, nil
	}
	ids := make([]string, 0, len(links))
	for _, l := range links {
		ids = append(ids, l.EventID)
	}
	events, err := (Events{EventRepository: r.Events, At: r.At}).ListByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("evidence events: %w", err)
	}
	known := make(map[string]bool, len(events))
	for _, ev := range events {
		known[ev.ID] = true
	}
	out := links[:0:0]
	for _, l := range links {
		if known[l.EventID] {
			out = append(out, l)
		}
	}
	return out, nil
}

// ListStatusHistoryByClaimID drops transitions after At, so lifecycle
// narratives end where the brain's knowledge did.
func (r Claims) ListStatusHistoryByClaimID(ctx context.Context, claimID string) ([]domain.ClaimStatusTransition, error) {
	history, err := r.ClaimRepository.ListStatusHistoryByClaimID(ctx, claimID)
	if err != nil {
		return nil, err
	}
	out := history[:0:0]
	for _, h := range history {
		if !h.ChangedAt.After(r.At) {
			out = append(out, h)
		}
	}
	return out, nil
}

// ListValidityHistory drops changes after At.
func (r Claims) ListValidityHistory(ctx context.Context, claimID string) ([]domain.ValidityChange, error) {
	history, err := r.ClaimRepository.ListValidityHistory(ctx, claimID)
	if err != nil {
		return nil, err
	}
	out := history[:0:0]
	for _, h := range history {
		if !h.ChangedAt.After(r.At) {
			out = append(out, h)
		}
	}
	return out, nil
}
