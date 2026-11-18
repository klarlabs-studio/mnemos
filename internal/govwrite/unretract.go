package govwrite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/retraction"
	"go.klarlabs.de/mnemos/internal/store"
)

// Event un-retraction (ADR 0037 §8).
//
// Un-retracting an event clears its retraction in force without deleting
// anything: an event_unretraction journal row is appended next to the
// retraction row it clears. What the retraction changed is undone only where
// nobody else has changed it since:
//
//   - a belief the retraction deprecated (its impact.retracted_beliefs) is
//     restored to its pre-retraction status when its latest status transition
//     is still the retraction's own (same reason, same actor), it has not been
//     merged, and it has live evidence again. Every other one is skipped, with
//     the reason;
//   - a belief the retraction rescored gets back the evidence link the
//     retraction removed, and is rescored. Its status is left alone.
//
// The journal row is written LAST, as for retraction: a failure part-way
// leaves the event retracted, and a re-run converges (a restored belief's
// latest transition is now the restoration, so it is skipped; relinking an
// existing link is a no-op).

const actionUnretractEvent = "unretract_event"

type unretractEventInput struct {
	EventID string
	Note    domain.ChangeNote
}

type unretractEventExecutor struct{ conn *store.Conn }

// Execute un-retracts the event and returns the report.
func (e unretractEventExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := payload[unretractEventInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("unretract_event: unexpected input %T", input)
	}
	rep, err := unretractEvent(ctx, e.conn, in.EventID, in.Note, time.Now().UTC())
	if err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("unretract event %s: %w", in.EventID, err)
	}
	return axidomain.ExecutionResult{
		Data: rep,
		Summary: fmt.Sprintf("un-retracted event %s: %d belief(s) restored, %d skipped, %d relinked (already un-retracted: %v)",
			in.EventID, len(rep.Impact.Restored), len(rep.Impact.Skipped), len(rep.Impact.Relinked), rep.AlreadyUnretracted),
	}, ev("mnemos.write.unretract_event", map[string]any{
		"event_id": in.EventID, "already_unretracted": rep.AlreadyUnretracted, "generation": rep.Generation,
		"restored_beliefs": len(rep.Impact.Restored), "skipped_beliefs": len(rep.Impact.Skipped),
		"relinked_beliefs": len(rep.Impact.Relinked),
		"reason":           in.Note.Reason, "changed_by": in.Note.ChangedBy,
	}), nil
}

// UnretractEvent clears eventID's retraction and reports what that restored
// (ADR 0037 §8). An event that was never retracted is an error wrapping
// retraction.ErrNotRetracted; one whose retraction is already cleared writes
// nothing and returns the stored record with AlreadyUnretracted set.
func (w *Writer) UnretractEvent(ctx context.Context, eventID string, note domain.ChangeNote) (domain.EventUnretraction, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return domain.EventUnretraction{}, errors.New("unretract event: event id is required")
	}
	return dispatch[domain.EventUnretraction](ctx, w, actionUnretractEvent, unretractEventInput{EventID: eventID, Note: note})
}

// ApplyUnretraction performs the un-retraction directly against conn, outside
// this package's kernel, for the root library's own governed kernel (see
// [ApplyRetraction]). Every other caller goes through [Writer.UnretractEvent].
func ApplyUnretraction(ctx context.Context, conn *store.Conn, eventID string, note domain.ChangeNote) (domain.EventUnretraction, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return domain.EventUnretraction{}, errors.New("unretract event: event id is required")
	}
	return unretractEvent(ctx, conn, eventID, note, time.Now().UTC())
}

func unretractEvent(ctx context.Context, conn *store.Conn, eventID string, note domain.ChangeNote, now time.Time) (domain.EventUnretraction, error) {
	history, err := retractionHistory(ctx, conn, eventID)
	if err != nil {
		return domain.EventUnretraction{}, err
	}
	active, ok := history.Active()
	if !ok {
		if prior, done := history.LastUnretraction(); done {
			prior.AlreadyUnretracted = true
			return prior, nil
		}
		return domain.EventUnretraction{}, fmt.Errorf("%w: %s", retraction.ErrNotRetracted, eventID)
	}
	event, err := conn.Events.GetByID(ctx, eventID)
	if err != nil {
		return domain.EventUnretraction{}, fmt.Errorf("event %s not found: %w", eventID, err)
	}

	rec := domain.EventUnretraction{
		EventID: eventID, UnretractedAt: now, UnretractedBy: actorOr(note.ChangedBy), Reason: note.Reason,
		Generation: active.Gen(), RetractedAt: active.RetractedAt, RetractedBy: active.RetractedBy,
	}
	impact := &rec.Impact
	impact.Restored = []domain.RestoredBelief{}
	impact.Skipped = []domain.SkippedBelief{}
	impact.Relinked = []domain.RelinkedBelief{}

	toRestore, err := restorable(ctx, conn, eventID, active, impact)
	if err != nil {
		return domain.EventUnretraction{}, err
	}
	if len(toRestore) > 0 {
		reason := domain.UnretractionReason(eventID, note.Reason)
		if err := conn.Claims.UpsertWithReasonAs(ctx, toRestore, reason, rec.UnretractedBy); err != nil {
			return domain.EventUnretraction{}, fmt.Errorf("restore beliefs: %w", err)
		}
	}

	relinked, err := relinkRescored(ctx, conn, eventID, active)
	if err != nil {
		return domain.EventUnretraction{}, err
	}
	rescoreIDs := make([]string, 0, len(toRestore)+len(relinked))
	for _, c := range toRestore {
		rescoreIDs = append(rescoreIDs, c.ID)
	}
	for _, c := range relinked {
		rescoreIDs = append(rescoreIDs, c.ID)
	}
	if err := rescoreKept(ctx, conn, rescoreIDs); err != nil {
		return domain.EventUnretraction{}, err
	}
	if len(relinked) > 0 {
		trustAfter, err := trustOf(ctx, conn, relinked)
		if err != nil {
			return domain.EventUnretraction{}, err
		}
		for _, c := range relinked {
			impact.Relinked = append(impact.Relinked, domain.RelinkedBelief{
				ID: c.ID, Status: c.Status, TrustBefore: c.TrustScore, TrustAfter: trustAfter[c.ID],
			})
		}
	}
	impact.Uncertainty = unretractionUncertainty(impact)

	data, err := json.Marshal(rec)
	if err != nil {
		return domain.EventUnretraction{}, fmt.Errorf("encode un-retraction: %w", err)
	}
	if err := conn.Journal.Append(ctx, []domain.JournalEntry{{
		ID: domain.UnretractionJournalID(eventID, rec.Generation), At: now, RunID: event.RunID,
		Kind: domain.JournalKindEventUnretraction, SubjectID: eventID, Data: string(data),
	}}); err != nil {
		return domain.EventUnretraction{}, fmt.Errorf("record un-retraction: %w", err)
	}
	return rec, nil
}

// restorable returns, set to their pre-retraction status, the beliefs active
// deprecated that nothing has changed since, and lists every other one in
// impact.Skipped with the reason.
func restorable(ctx context.Context, conn *store.Conn, eventID string, active domain.EventRetraction, impact *domain.UnretractionImpact) ([]domain.Claim, error) {
	candidates := active.Impact.RetractedBeliefs
	if len(candidates) == 0 {
		return nil, nil
	}
	claims, err := conn.Claims.ListByIDs(ctx, candidates)
	if err != nil {
		return nil, fmt.Errorf("read retracted beliefs: %w", err)
	}
	byID := make(map[string]domain.Claim, len(claims))
	for _, c := range claims {
		byID[c.ID] = c
	}
	live, err := liveEvidenceRestored(ctx, conn, claims, eventID)
	if err != nil {
		return nil, err
	}
	wantReason := domain.RetractionReason(eventID, active.Reason)

	var out []domain.Claim
	skip := func(id, why string) {
		impact.Skipped = append(impact.Skipped, domain.SkippedBelief{ID: id, Reason: why})
	}
	for _, id := range candidates {
		c, ok := byID[id]
		if !ok {
			skip(id, "belief no longer exists")
			continue
		}
		if c.MergedInto != "" {
			skip(id, "merged into "+c.MergedInto+" since the retraction")
			continue
		}
		history, err := conn.Claims.ListStatusHistoryByClaimID(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("status history of %s: %w", id, err)
		}
		if len(history) == 0 {
			skip(id, "no status history records the retraction")
			continue
		}
		last := history[len(history)-1]
		// A retraction only deprecates beliefs that were not deprecated, so
		// its own row is always a change. A reassertion after it is someone
		// deciding the status again (ADR 0037 §8): their decision stands.
		if last.IsReassertion() {
			skip(id, fmt.Sprintf("status reasserted as %s since the retraction (%s by %s); that decision stands",
				last.ToStatus, last.Reason, actorOr(last.ChangedBy)))
			continue
		}
		if c.Status != domain.ClaimStatusDeprecated || last.ToStatus != domain.ClaimStatusDeprecated ||
			last.Reason != wantReason || last.ChangedBy != active.RetractedBy {
			skip(id, fmt.Sprintf("status changed since the retraction: now %s (%s by %s)", c.Status, last.Reason, actorOr(last.ChangedBy)))
			continue
		}
		if live[id] == 0 {
			skip(id, "no live evidence once the episode is restored")
			continue
		}
		prior := last.FromStatus
		if prior == "" || prior == domain.ClaimStatusDeprecated {
			prior = domain.ClaimStatusActive
		}
		c.Status = prior
		out = append(out, c)
		impact.Restored = append(impact.Restored, domain.RestoredBelief{ID: id, Status: prior})
	}
	return out, nil
}

// liveEvidenceRestored counts, per belief, its evidence links to events that
// are not retracted, counting eventID (about to be restored) as live.
func liveEvidenceRestored(ctx context.Context, conn *store.Conn, claims []domain.Claim, eventID string) (map[string]int, error) {
	out := make(map[string]int, len(claims))
	if len(claims) == 0 {
		return out, nil
	}
	ids := make([]string, len(claims))
	for i, c := range claims {
		ids[i] = c.ID
	}
	links, err := conn.Claims.ListEvidenceByClaimIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list evidence of retracted beliefs: %w", err)
	}
	retracted := map[string]bool{eventID: false}
	for _, l := range links {
		dead, seen := retracted[l.EventID]
		if !seen {
			_, isRetracted, err := LookupRetraction(ctx, conn, l.EventID)
			if err != nil {
				return nil, err
			}
			retracted[l.EventID] = isRetracted
			dead = isRetracted
		}
		if !dead {
			out[l.ClaimID]++
		}
	}
	return out, nil
}

// relinkRescored gives every belief active rescored back its evidence link to
// eventID and returns them as they were before relinking (trust included).
// Beliefs that no longer exist are dropped.
func relinkRescored(ctx context.Context, conn *store.Conn, eventID string, active domain.EventRetraction) ([]domain.Claim, error) {
	if len(active.Impact.Rescored) == 0 {
		return nil, nil
	}
	ids := make([]string, len(active.Impact.Rescored))
	for i, r := range active.Impact.Rescored {
		ids[i] = r.ID
	}
	claims, err := conn.Claims.ListByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("read rescored beliefs: %w", err)
	}
	sort.Slice(claims, func(i, j int) bool { return claims[i].ID < claims[j].ID })
	links := make([]domain.ClaimEvidence, len(claims))
	for i, c := range claims {
		links[i] = domain.ClaimEvidence{ClaimID: c.ID, EventID: eventID}
	}
	if err := conn.Claims.UpsertEvidence(ctx, links); err != nil {
		return nil, fmt.Errorf("relink rescored beliefs: %w", err)
	}
	return claims, nil
}

func trustOf(ctx context.Context, conn *store.Conn, claims []domain.Claim) (map[string]float64, error) {
	ids := make([]string, len(claims))
	for i, c := range claims {
		ids[i] = c.ID
	}
	after, err := conn.Claims.ListByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("read relinked beliefs: %w", err)
	}
	out := make(map[string]float64, len(after))
	for _, c := range after {
		out[c.ID] = c.TrustScore
	}
	return out, nil
}

// unretractionUncertainty states what the report does not see or claim.
func unretractionUncertainty(impact *domain.UnretractionImpact) []string {
	out := []string{
		"Associations, decisions and outcome edges were never rewritten by the retraction, so nothing is restored for them; a decision reviewed because of the retraction is not re-judged.",
		"Trust is recomputed on the evidence each belief has now, not restored from before the retraction.",
	}
	if len(impact.Skipped) > 0 {
		out = append(out, "A skipped belief was changed after the retraction by something else (a person, a merge, another retraction); its status is that change's, so un-retraction leaves it.")
	}
	for _, r := range impact.Relinked {
		if r.Status == domain.ClaimStatusDeprecated {
			out = append(out, "A relinked belief is deprecated by something other than this retraction; it regained this episode as evidence but stays deprecated.")
			break
		}
	}
	return out
}
