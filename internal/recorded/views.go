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
// Closed links (ADR 0036 §8). A link closed after At — by a retraction, or by
// the merge that absorbed its claim — is evidence at At, and its claim is
// reached through its event. Evidence on events ingested after At is dropped
// when Events is set.
//
// Merges (ADR 0036 step 2). Semantic dedupe moves a loser's evidence onto the
// winner and keeps the loser as a redirect. Since §8 the merge closes the
// loser's own links, so a loser merged after At reads exactly its own
// evidence through the closed links above. A merge recorded before §8 left no
// closed links; for such a loser the view falls back to walking the redirect
// backwards:
//
//   - ListByEventIDs reaches the claim that holds the evidence now and adds
//     every such claim merged into it (transitively) that the brain still held
//     separately at At, i.e. merged after At: it is reached through all of its
//     winner's evidence, not only its own, an over-approximation between
//     claims that were near-duplicates, which the merge required.
//   - ListEvidenceByClaimIDs attributes such a claim the evidence of the claim
//     that absorbed it, for the same reason.
type Claims struct {
	ports.ClaimRepository
	// Events, when set, bounds evidence links to events ingested by At.
	Events ports.EventRepository
	// Dependencies, when set, is source dependence between episodes (ADR
	// 0038): trust at At does not count a dependent evidence episode as a
	// distinct source when its dependency was recorded by At and not removed
	// by At. Leave it unset
	// only on a store that has none, or reconstructed trust overstates
	// corroboration that the live score does not.
	Dependencies ports.EventDependencyRepository
	At           time.Time
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

// scoreAt returns claims as the brain scored them at At. It rewinds the trust
// inputs the claims row stores itself (confidence, half-life, last
// confirmation) from the trust-input history (StoredInputsAt), then replaces
// the stored trust_score, today's, with the trust at At (TrustAt). Trust reads
// the claims' evidence through the view, so merges and the event bound apply,
// the episodes those links name and the credit history. Without Events the
// view cannot date evidence and the stored score stands; both callers (recall,
// browse) set it.
func (r Claims) scoreAt(ctx context.Context, claims []domain.Claim) ([]domain.Claim, error) {
	if len(claims) == 0 {
		return claims, nil
	}
	ids := make([]string, len(claims))
	for i, c := range claims {
		ids[i] = c.ID
	}
	var inputs []domain.TrustInputChange
	if lister, ok := r.ClaimRepository.(ports.TrustInputHistoryLister); ok {
		var err error
		if inputs, err = lister.ListTrustInputHistory(ctx, ids); err != nil {
			return nil, fmt.Errorf("trust input history: %w", err)
		}
	}
	inputsOf := make(map[string][]domain.TrustInputChange)
	for _, ch := range inputs {
		inputsOf[ch.ClaimID] = append(inputsOf[ch.ClaimID], ch)
	}
	if r.Events != nil {
		if err := r.trustAt(ctx, claims, ids, inputsOf); err != nil {
			return nil, err
		}
	}
	for i := range claims {
		c := &claims[i]
		c.Confidence, c.HalfLifeDays, c.LastConfirmed = StoredInputsAt(*c, inputsOf[c.ID], r.At)
	}
	return claims, nil
}

// trustAt sets each claim's TrustScore to its trust at At.
func (r Claims) trustAt(ctx context.Context, claims []domain.Claim, ids []string, inputsOf map[string][]domain.TrustInputChange) error {
	links, err := r.ListEvidenceByClaimIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("evidence for trust at %s: %w", r.At.Format(time.RFC3339), err)
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
			return fmt.Errorf("evidence events for trust: %w", err)
		}
		for _, ev := range evs {
			events[ev.ID] = ev
		}
	}
	var deps []domain.EventDependency
	if r.Dependencies != nil && len(eventIDs) > 0 {
		// Removed dependencies too: one removed after At was in force at At.
		if deps, err = r.Dependencies.ListByEventIDsWithRemoved(ctx, eventIDs); err != nil {
			return fmt.Errorf("episode dependencies for trust: %w", err)
		}
	}
	var credit []domain.CreditChange
	if lister, ok := r.ClaimRepository.(ports.CreditHistoryLister); ok {
		if credit, err = lister.ListCreditHistory(ctx, ids); err != nil {
			return fmt.Errorf("credit history: %w", err)
		}
	}
	creditOf := make(map[string][]domain.CreditChange)
	for _, ch := range credit {
		creditOf[ch.ClaimID] = append(creditOf[ch.ClaimID], ch)
	}
	for i := range claims {
		c := &claims[i]
		c.TrustScore = TrustAt(*c, byClaim[c.ID], events, deps, creditOf[c.ID], inputsOf[c.ID], r.At)
		c.TrustComputedAt = r.At
		c.TrustModelVersion = trust.ModelVersion
	}
	return nil
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
// holding their evidence now, the claims whose link to one of them was in
// force at At and has been closed since (ADR 0036 §8), plus every claim merged
// into those after At, which held its own evidence then.
func (r Claims) ListByEventIDs(ctx context.Context, eventIDs []string) ([]domain.Claim, error) {
	holders, err := r.ClaimRepository.ListByEventIDs(ctx, eventIDs)
	if err != nil {
		return nil, err
	}
	if holders, err = r.withClosedHolders(ctx, holders, eventIDs); err != nil {
		return nil, err
	}
	linked, err := r.linkedBy(ctx, holders, eventIDs)
	if err != nil {
		return nil, err
	}
	out, err := r.rewind(ctx, linked)
	if err != nil {
		return nil, err
	}
	merged, err := r.mergedAway(ctx, holders)
	if err != nil {
		return nil, err
	}
	// A loser whose merge closed its links is reached exactly, through
	// withClosedHolders; only a loser merged before that walks the redirect.
	if merged, err = r.withoutExactMerges(ctx, merged); err != nil {
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

// linkedBy keeps the holders whose link to one of eventIDs was recorded by At
// (a link with no recorded time is kept: LinkRecordedAt dates it, and the
// claim and event bounds already apply). A winner that gained an episode in a
// merge after At, or a claim that a restatement linked after At, did not hold
// it at At. Closed links in force at At were added by withClosedHolders and
// count too.
func (r Claims) linkedBy(ctx context.Context, holders []domain.Claim, eventIDs []string) ([]domain.Claim, error) {
	if len(holders) == 0 {
		return holders, nil
	}
	ids := make([]string, len(holders))
	for i, c := range holders {
		ids[i] = c.ID
	}
	links, err := r.ClaimRepository.ListEvidenceByClaimIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("evidence of event holders: %w", err)
	}
	closed, err := r.ListClosedEvidenceByClaimIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("closed evidence of event holders: %w", err)
	}
	for _, l := range closed {
		if l.InForceAt(r.At) {
			links = append(links, l.BeliefEvidence)
		}
	}
	wanted := make(map[string]bool, len(eventIDs))
	for _, id := range eventIDs {
		wanted[id] = true
	}
	held := map[string]bool{}
	for _, l := range links {
		if wanted[l.EventID] && (l.CreatedAt.IsZero() || !l.CreatedAt.After(r.At)) {
			held[l.ClaimID] = true
		}
	}
	out := holders[:0:0]
	for _, c := range holders {
		if held[c.ID] {
			out = append(out, c)
		}
	}
	return out, nil
}

// withoutExactMerges drops the claims whose merge closed their own links
// (ADR 0036 §8): their evidence at At is known exactly.
func (r Claims) withoutExactMerges(ctx context.Context, claims []domain.Claim) ([]domain.Claim, error) {
	if len(claims) == 0 {
		return claims, nil
	}
	ids := make([]string, len(claims))
	for i, c := range claims {
		ids[i] = c.ID
	}
	closed, err := r.ListClosedEvidenceByClaimIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("closed evidence of merged claims: %w", err)
	}
	exact := exactMerges(closed)
	out := claims[:0:0]
	for _, c := range claims {
		if !exact[c.ID] {
			out = append(out, c)
		}
	}
	return out, nil
}

// exactMerges returns the claims among closed whose merge closed their links.
func exactMerges(closed []domain.ClosedEvidence) map[string]bool {
	out := map[string]bool{}
	for _, l := range closed {
		if domain.IsMergeClosureOf(l.ClaimID, l.Reason) {
			out[l.ClaimID] = true
		}
	}
	return out
}

// withClosedHolders adds to holders the claims whose link to one of eventIDs
// was in force at At but is closed now.
func (r Claims) withClosedHolders(ctx context.Context, holders []domain.Claim, eventIDs []string) ([]domain.Claim, error) {
	closed, err := r.ListClosedEvidenceByEventIDs(ctx, eventIDs)
	if err != nil {
		return nil, fmt.Errorf("closed evidence of events: %w", err)
	}
	have := make(map[string]bool, len(holders))
	for _, c := range holders {
		have[c.ID] = true
	}
	var missing []string
	for _, l := range closed {
		if l.InForceAt(r.At) && !have[l.ClaimID] {
			have[l.ClaimID] = true
			missing = append(missing, l.ClaimID)
		}
	}
	if len(missing) == 0 {
		return holders, nil
	}
	more, err := r.ClaimRepository.ListByIDs(ctx, missing)
	if err != nil {
		return nil, err
	}
	return append(holders, more...), nil
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

// ListEvidenceByClaimIDs returns the evidence links of ids as they stood at At:
// the live links and the closed links that were in force at At (ADR 0036 §8).
// A claim merged after At by a merge recorded before §8 is attributed the
// evidence of the claim that holds it now (see Claims).
func (r Claims) ListEvidenceByClaimIDs(ctx context.Context, ids []string) ([]domain.ClaimEvidence, error) {
	roots, err := r.heldRoots(ctx, ids)
	if err != nil {
		return nil, err
	}
	closed, err := r.ListClosedEvidenceByClaimIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("closed evidence: %w", err)
	}
	exact := exactMerges(closed)
	query := append([]string(nil), ids...)
	heldBy := map[string][]string{} // root -> merged claims it stands in for
	for id, root := range roots {
		if exact[id] {
			continue
		}
		query = append(query, root)
		heldBy[root] = append(heldBy[root], id)
	}
	links, err := r.ClaimRepository.ListEvidenceByClaimIDs(ctx, query)
	if err != nil {
		return nil, err
	}
	if extra := query[len(ids):]; len(extra) > 0 {
		rootsClosed, err := r.ListClosedEvidenceByClaimIDs(ctx, extra)
		if err != nil {
			return nil, fmt.Errorf("closed evidence of merge roots: %w", err)
		}
		closed = append(closed, rootsClosed...)
	}
	for _, l := range closed {
		if l.InForceAt(r.At) {
			links = append(links, l.BeliefEvidence)
		}
	}
	asked := make(map[string]bool, len(ids))
	for _, id := range ids {
		asked[id] = true
	}
	ev := evidenceAt{at: r.At, index: map[[2]string]int{}}
	for _, l := range links {
		if asked[l.ClaimID] {
			ev.add(l)
		}
		for _, merged := range heldBy[l.ClaimID] {
			ev.add(domain.ClaimEvidence{ClaimID: merged, EventID: l.EventID, CreatedAt: l.CreatedAt})
		}
	}
	// A link recorded after At was not evidence at At: a winner's link gained
	// in a later merge, or a restatement linked later. A link with no recorded
	// time is left to LinkRecordedAt.
	kept := ev.out[:0]
	for _, l := range ev.out {
		if ev.recordedBy(l) {
			kept = append(kept, l)
		}
	}
	return r.recordedEvidence(ctx, kept)
}

// evidenceAt collects one link per (claim, event). When the same pair arrives
// twice (a link closed by a retraction and reopened by its un-retraction, or a
// merged claim's own link and its absorber's), the one recorded by at wins, so
// the link in force at at is the one returned.
type evidenceAt struct {
	at    time.Time
	index map[[2]string]int
	out   []domain.ClaimEvidence
}

func (e *evidenceAt) add(l domain.ClaimEvidence) {
	i, ok := e.index[l.Key()]
	if !ok {
		e.index[l.Key()] = len(e.out)
		e.out = append(e.out, l)
		return
	}
	if e.recordedBy(l) && !e.recordedBy(e.out[i]) {
		e.out[i] = l
	}
}

// recordedBy reports whether l was recorded by at; a link with no recorded
// time is left to LinkRecordedAt.
func (e *evidenceAt) recordedBy(l domain.ClaimEvidence) bool {
	return l.CreatedAt.IsZero() || !l.CreatedAt.After(e.at)
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
