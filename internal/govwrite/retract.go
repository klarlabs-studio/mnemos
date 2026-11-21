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

	"go.klarlabs.de/mnemos/internal/clock"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/retraction"
	"go.klarlabs.de/mnemos/internal/store"
)

// Event retraction (ADR 0037).
//
// Retracting an event withdraws it as evidence without touching the event row,
// which is immutable. The retraction is recorded once, append-only, in the
// cognitive journal (kind domain.JournalKindEventRetraction, subject = the
// event id), with who, when, why and the impact report. Dependents:
//
//   - a belief with no other LIVE evidence (evidence from an event that is not
//     itself retracted) is retracted: a status transition to deprecated whose
//     reason starts with domain.RetractionReasonPrefix (ADR 0034's "retracted",
//     never true). Its evidence link is kept, so a recorded-as-of read before the
//     retraction (ADR 0036) still reaches the belief through the event and
//     rewinds it to its earlier status;
//   - a belief with other live evidence loses the link to the event and is
//     rescored on what remains (RecomputeTrustForClaims). The link is closed,
//     not deleted (ClaimRepository.CloseEvidence, ADR 0036 §8), so a
//     recorded-as-of read before the retraction still counts it;
//   - associations, decisions and outcome edges that touch a retracted belief
//     are listed in the report, never rewritten.
//
// The journal row is written LAST. A failure part-way leaves no record, so a
// re-run finds the event unretracted and converges: deprecating an already
// deprecated belief and closing a link that is no longer live are both no-ops.

const actionRetractEvent = "retract_event"

type retractEventInput struct {
	EventID string
	Note    domain.ChangeNote
}

type retractEventExecutor struct{ conn *store.Conn }

// Execute retracts the event and returns the impact report.
func (e retractEventExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := payload[retractEventInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("retract_event: unexpected input %T", input)
	}
	rep, err := retractEvent(ctx, e.conn, in.EventID, in.Note, clock.Now().UTC())
	if err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("retract event %s: %w", in.EventID, err)
	}
	return axidomain.ExecutionResult{
		Data: rep,
		Summary: fmt.Sprintf("retracted event %s: %d belief(s) retracted, %d rescored (already retracted: %v)",
			in.EventID, len(rep.Impact.RetractedBeliefs), len(rep.Impact.Rescored), rep.AlreadyRetracted),
	}, ev("mnemos.write.retract_event", map[string]any{
		"event_id": in.EventID, "already_retracted": rep.AlreadyRetracted,
		"retracted_beliefs": len(rep.Impact.RetractedBeliefs), "rescored_beliefs": len(rep.Impact.Rescored),
		"reason": in.Note.Reason, "changed_by": in.Note.ChangedBy,
	}), nil
}

// RetractEvent withdraws eventID as evidence and reports what that changed
// (ADR 0037). It is the one implementation of retraction: the CLI, the MCP
// tool and watch_file all call it. Retracting an already retracted event
// writes nothing and returns the stored record with AlreadyRetracted set.
func (w *Writer) RetractEvent(ctx context.Context, eventID string, note domain.ChangeNote) (domain.EventRetraction, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return domain.EventRetraction{}, errors.New("retract event: event id is required")
	}
	return dispatch[domain.EventRetraction](ctx, w, actionRetractEvent, retractEventInput{EventID: eventID, Note: note})
}

// ApplyRetraction performs the retraction directly against conn, outside this
// package's kernel. It exists for the root library, whose public writes are
// governed by the library's own kernel: its executor calls this so the library
// and the daemon share one implementation. Every other caller goes through
// [Writer.RetractEvent].
func ApplyRetraction(ctx context.Context, conn *store.Conn, eventID string, note domain.ChangeNote) (domain.EventRetraction, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return domain.EventRetraction{}, errors.New("retract event: event id is required")
	}
	return retractEvent(ctx, conn, eventID, note, clock.Now().UTC())
}

// LookupRetraction returns eventID's retraction in force now, and false when
// the event is not retracted: never retracted, or its latest retraction was
// cleared by an un-retraction (ADR 0037 §8).
func LookupRetraction(ctx context.Context, conn *store.Conn, eventID string) (domain.EventRetraction, bool, error) {
	history, err := retractionHistory(ctx, conn, eventID)
	if err != nil {
		return domain.EventRetraction{}, false, err
	}
	rec, ok := history.Active()
	return rec, ok, nil
}

func retractionHistory(ctx context.Context, conn *store.Conn, eventID string) (domain.RetractionHistory, error) {
	if conn == nil {
		return domain.RetractionHistory{}, errors.New("this backend has no journal to record retractions in")
	}
	return retraction.LoadHistory(ctx, conn.Journal, eventID)
}

func retractEvent(ctx context.Context, conn *store.Conn, eventID string, note domain.ChangeNote, now time.Time) (domain.EventRetraction, error) {
	history, err := retractionHistory(ctx, conn, eventID)
	if err != nil {
		return domain.EventRetraction{}, err
	}
	if prior, ok := history.Active(); ok {
		prior.AlreadyRetracted = true
		return prior, nil
	}
	gen := history.NextGeneration()
	event, err := conn.Events.GetByID(ctx, eventID)
	if err != nil {
		return domain.EventRetraction{}, fmt.Errorf("event %s not found: %w", eventID, err)
	}

	direct, err := conn.Claims.ListByEventIDs(ctx, []string{eventID})
	if err != nil {
		return domain.EventRetraction{}, fmt.Errorf("list dependent beliefs: %w", err)
	}
	// ADR 0038 §4: the episodes that depend on this one, and the beliefs
	// their evidence reaches.
	closure, err := closureOf(ctx, conn, eventID)
	if err != nil {
		return domain.EventRetraction{}, err
	}
	reached, via, err := reachedThroughClosure(ctx, conn, closure, direct)
	if err != nil {
		return domain.EventRetraction{}, err
	}
	dependents := make([]domain.Claim, 0, len(direct)+len(reached))
	dependents = append(append(dependents, direct...), reached...)
	live, err := liveIndependentEvidence(ctx, conn, dependents, newIndependence(conn, map[string]bool{eventID: true}))
	if err != nil {
		return domain.EventRetraction{}, err
	}

	rec := domain.EventRetraction{
		EventID: eventID, RetractedAt: now, RetractedBy: actorOr(note.ChangedBy), Reason: note.Reason,
		Generation: gen,
	}
	impact := &rec.Impact
	impact.RetractedBeliefs = []string{}
	impact.Rescored = []domain.RescoredBelief{}
	impact.DependentEpisodes = closure.hops()

	isDirect := make(map[string]bool, len(direct))
	for _, c := range direct {
		isDirect[c.ID] = true
	}
	pathOf := func(id string) []string {
		if isDirect[id] {
			return []string{eventID}
		}
		return closure.path(via[id])
	}

	var toRetract []domain.Claim
	var kept []domain.Claim
	for _, c := range dependents {
		switch {
		case live[c.ID] > 0 && isDirect[c.ID]:
			kept = append(kept, c)
			impact.BeliefPaths = append(impact.BeliefPaths, domain.BeliefPath{BeliefID: c.ID, Outcome: domain.BeliefOutcomeRescored, Via: pathOf(c.ID)})
		case live[c.ID] > 0:
			// Reached only through a dependent episode, and kept on
			// independent evidence. Nothing changes: the dependent's
			// evidence never counted as independent (ADR 0038 §4.6).
			impact.BeliefPaths = append(impact.BeliefPaths, domain.BeliefPath{BeliefID: c.ID, Outcome: domain.BeliefOutcomeKept, Via: pathOf(c.ID)})
		case c.Status == domain.ClaimStatusDeprecated:
			impact.AlreadyDeprecated = append(impact.AlreadyDeprecated, c.ID)
		default:
			c.Status = domain.ClaimStatusDeprecated
			toRetract = append(toRetract, c)
			impact.RetractedBeliefs = append(impact.RetractedBeliefs, c.ID)
			impact.BeliefPaths = append(impact.BeliefPaths, domain.BeliefPath{BeliefID: c.ID, Outcome: domain.BeliefOutcomeRetracted, Via: pathOf(c.ID)})
		}
	}

	if len(toRetract) > 0 {
		reason := domain.RetractionReason(eventID, note.Reason)
		if err := conn.Claims.UpsertWithReasonAs(ctx, toRetract, reason, rec.RetractedBy); err != nil {
			return domain.EventRetraction{}, fmt.Errorf("retract dependent beliefs: %w", err)
		}
	}

	// A rescored belief's link to the retracted event is closed, not deleted
	// (ADR 0036 §8): live reads lose it, and a recorded-as-of read from before
	// the retraction still sees it.
	closeNote := domain.ChangeNote{Reason: domain.RetractionReason(eventID, note.Reason), ChangedBy: rec.RetractedBy}
	keptIDs := make([]string, len(kept))
	for i, c := range kept {
		keptIDs[i] = c.ID
		if err := conn.Claims.CloseEvidence(ctx, c.ID, eventID, now, closeNote); err != nil {
			return domain.EventRetraction{}, fmt.Errorf("close link %s -> %s: %w", c.ID, eventID, err)
		}
	}
	if err := rescoreKept(ctx, conn, keptIDs); err != nil {
		return domain.EventRetraction{}, err
	}
	if len(keptIDs) > 0 {
		after, err := conn.Claims.ListByIDs(ctx, keptIDs)
		if err != nil {
			return domain.EventRetraction{}, fmt.Errorf("read rescored beliefs: %w", err)
		}
		trustAfter := make(map[string]float64, len(after))
		for _, c := range after {
			trustAfter[c.ID] = c.TrustScore
		}
		for _, c := range kept {
			impact.Rescored = append(impact.Rescored, domain.RescoredBelief{
				ID: c.ID, TrustBefore: c.TrustScore, TrustAfter: trustAfter[c.ID], RemainingEvidence: live[c.ID],
			})
		}
	}

	if err := collectExposure(ctx, conn, impact); err != nil {
		return domain.EventRetraction{}, err
	}
	impact.Uncertainty = retractionUncertainty(impact)

	data, err := json.Marshal(rec)
	if err != nil {
		return domain.EventRetraction{}, fmt.Errorf("encode retraction: %w", err)
	}
	if err := conn.Journal.Append(ctx, []domain.JournalEntry{{
		ID: domain.RetractionJournalIDGen(eventID, gen), At: now, RunID: event.RunID,
		Kind: domain.JournalKindEventRetraction, SubjectID: eventID, Data: string(data),
	}}); err != nil {
		return domain.EventRetraction{}, fmt.Errorf("record retraction: %w", err)
	}
	return rec, nil
}

// reachedThroughClosure returns the beliefs with evidence from an episode in
// closure that are not already in direct, sorted by id, and for each the
// closure episode that reached it (the nearest one, then the smallest id).
func reachedThroughClosure(ctx context.Context, conn *store.Conn, closure dependencyClosure, direct []domain.Claim) ([]domain.Claim, map[string]string, error) {
	via := map[string]string{}
	if len(closure.order) == 0 {
		return nil, via, nil
	}
	claims, err := conn.Claims.ListByEventIDs(ctx, closure.order)
	if err != nil {
		return nil, nil, fmt.Errorf("list beliefs of dependent episodes: %w", err)
	}
	skip := make(map[string]bool, len(direct))
	for _, c := range direct {
		skip[c.ID] = true
	}
	var out []domain.Claim
	for _, c := range claims {
		if !skip[c.ID] {
			skip[c.ID] = true
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, via, nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	ids := make([]string, len(out))
	for i, c := range out {
		ids[i] = c.ID
	}
	links, err := conn.Claims.ListEvidenceByClaimIDs(ctx, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("list evidence of reached beliefs: %w", err)
	}
	rank := make(map[string]int, len(closure.order))
	for i, id := range closure.order {
		rank[id] = i
	}
	for _, l := range links {
		r, inClosure := rank[l.EventID]
		if !inClosure {
			continue
		}
		if cur, ok := via[l.ClaimID]; !ok || r < rank[cur] {
			via[l.ClaimID] = l.EventID
		}
	}
	return out, via, nil
}

// collectExposure lists what touches a retracted belief: associations,
// decisions that cited it, and outcome edges onto it. Read only.
func collectExposure(ctx context.Context, conn *store.Conn, impact *domain.RetractionImpact) error {
	if len(impact.RetractedBeliefs) == 0 {
		return nil
	}
	retracted := make(map[string]bool, len(impact.RetractedBeliefs))
	for _, id := range impact.RetractedBeliefs {
		retracted[id] = true
	}

	rels, err := conn.Relationships.ListByClaimIDs(ctx, impact.RetractedBeliefs)
	if err != nil {
		return fmt.Errorf("list associations of retracted beliefs: %w", err)
	}
	seen := map[string]bool{}
	for _, r := range rels {
		if !seen[r.ID] {
			seen[r.ID] = true
			impact.Associations = append(impact.Associations, r.ID)
		}
	}
	sort.Strings(impact.Associations)

	if conn.Decisions != nil {
		decisions, err := conn.Decisions.ListAll(ctx)
		if err != nil {
			return fmt.Errorf("list decisions: %w", err)
		}
		for _, d := range decisions {
			var cited []string
			for _, b := range d.Beliefs {
				if retracted[b] {
					cited = append(cited, b)
				}
			}
			if len(cited) > 0 {
				sort.Strings(cited)
				impact.Decisions = append(impact.Decisions, domain.DecisionExposure{DecisionID: d.ID, Beliefs: cited})
			}
		}
		sort.Slice(impact.Decisions, func(i, j int) bool { return impact.Decisions[i].DecisionID < impact.Decisions[j].DecisionID })
	}

	if conn.EntityRels != nil {
		for _, id := range impact.RetractedBeliefs {
			edges, err := conn.EntityRels.ListByEntity(ctx, id, domain.RelEntityClaim)
			if err != nil {
				return fmt.Errorf("list outcome edges of %s: %w", id, err)
			}
			for _, e := range edges {
				if e.Kind == domain.RelationshipTypeValidates || e.Kind == domain.RelationshipTypeRefutes {
					impact.OutcomeEdges = append(impact.OutcomeEdges, e.ID)
				}
			}
		}
		sort.Strings(impact.OutcomeEdges)
	}
	return nil
}

// retractionUncertainty states what the report does not see or claim.
func retractionUncertainty(impact *domain.RetractionImpact) []string {
	out := []string{
		"Schemas (lessons) and reflexes (playbooks) are derived from actions and outcomes " +
			"(lesson_evidence.action_id, playbook_lessons), not from episodes or beliefs, so no stored " +
			"link reaches them from a retracted episode and none are listed; that is an absence of a link, not a finding that none depended on it.",
		"Beliefs derived from a retracted belief by an association are not re-evaluated; the associations are listed, not judged.",
		"Dependent episodes are found only where an episode says what it depends on (a citation marker naming a stored source, " +
			"a link, or provenance metadata; ADR 0038). An uncited paraphrase or an uncited conclusion drawn from this episode " +
			"is not detected and still counts as independent evidence.",
	}
	if len(impact.Decisions) > 0 {
		out = append(out, "A listed decision cited a retracted belief. That does not make the decision wrong: "+
			"it may rest on other beliefs, and its recorded outcome is what judges it.")
	}
	if len(impact.Rescored) > 0 {
		out = append(out, "Rescored trust reflects the evidence that remains; a belief kept on one remaining episode is only as sound as that episode.")
	}
	return out
}

// actorOr returns actor, or the system sentinel when it is empty.
func actorOr(actor string) string {
	if strings.TrimSpace(actor) == "" {
		return domain.SystemUser
	}
	return actor
}
