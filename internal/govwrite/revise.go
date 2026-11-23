package govwrite

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/revision"
	"go.klarlabs.de/mnemos/internal/store"
)

// Revision kinds, as reported in [Revision.Kind].
const (
	RevisionFlag      = string(revision.StepFlag)
	RevisionSupersede = string(revision.StepSupersede)
	RevisionClear     = string(revision.StepClear)
)

// reviseSurprised runs surprise-triggered revision (ADR 0049) after a
// reconcile pass and attaches each revision to the reconciliation it came
// from. The decision rules are internal/revision's; this function only reads
// the inputs and applies the plan.
//
// It re-plans over EVERY refuted decision expectation, not only the ones just
// reconciled. A surprise that had no contrary evidence when it happened only
// flagged its beliefs; when a newer contradicting belief arrives later, the
// next pass supersedes the flagged belief. Such a revision is reported as a
// Reconciliation with Rechecked set. Applying a plan and re-planning yields no
// further steps, so a pass with nothing new to revise writes nothing.
func reviseSurprised(ctx context.Context, conn *store.Conn, recs []Reconciliation, now time.Time, actor string) ([]Reconciliation, error) {
	if conn.Decisions == nil || conn.Expectations == nil || conn.Relationships == nil {
		return recs, nil
	}
	surprises, exps, err := refutedDecisionExpectations(ctx, conn)
	if err != nil || len(surprises) == 0 {
		return recs, err
	}
	in, claims, err := revisionInputs(ctx, conn, surprises)
	if err != nil {
		return recs, err
	}
	plan := revision.Plan(in.Inputs)
	if len(plan.Steps) == 0 {
		return recs, nil
	}
	if actor == "" {
		actor = domain.SystemUser
	}
	byDecision := map[string][]Revision{}
	for _, step := range plan.Steps {
		if err := applyRevision(ctx, conn, step, claims, plan.ExplainedBy, now, actor); err != nil {
			return recs, err
		}
		for _, d := range step.DecisionIDs {
			byDecision[d] = append(byDecision[d], Revision{ClaimID: step.ClaimID, Kind: string(step.Kind), By: step.By})
		}
	}
	reported := map[string]bool{}
	for i := range recs {
		if recs[i].DecisionID == "" {
			continue
		}
		recs[i].Revisions = byDecision[recs[i].DecisionID]
		reported[recs[i].DecisionID] = true
	}
	var rechecked []string
	for d := range byDecision {
		if !reported[d] {
			rechecked = append(rechecked, d)
		}
	}
	sort.Strings(rechecked)
	for _, d := range rechecked {
		exp := exps[d]
		surprise, _ := exp.Surprise()
		recs = append(recs, Reconciliation{
			ClaimID: exp.ClaimID, Predicted: exp.Predicted, Observed: exp.Observed, Surprise: surprise,
			DecisionID: d, AttributedTo: in.surprisedRestsOn(d), Revisions: byDecision[d], Rechecked: true,
		})
	}
	return recs, nil
}

// revisionInputsWithRestsOn wraps revision.Inputs with a lookup used for
// reporting.
type revisionInputsWithRestsOn struct {
	revision.Inputs
	restsOn map[string][]string
}

func (in revisionInputsWithRestsOn) surprisedRestsOn(decisionID string) []string {
	return in.restsOn[decisionID]
}

// refutedDecisionExpectations returns every resolved decision expectation
// whose observation fell outside its tolerance, as revision surprises, plus
// the expectation of each by decision id.
func refutedDecisionExpectations(ctx context.Context, conn *store.Conn) ([]revision.Surprise, map[string]domain.Expectation, error) {
	decisions, err := conn.Decisions.ListAll(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("revise: list decisions: %w", err)
	}
	var out []revision.Surprise
	exps := map[string]domain.Expectation{}
	for _, d := range decisions {
		if len(d.Beliefs) == 0 {
			continue
		}
		exp, ok, err := conn.Expectations.Get(ctx, domain.DecisionExpectationKey(d.ID))
		if err != nil {
			return nil, nil, fmt.Errorf("revise: get expectation of decision %s: %w", d.ID, err)
		}
		if !ok || !exp.Resolved {
			continue
		}
		if confirmed, observed := exp.Confirmed(); !observed || confirmed {
			continue // confirmed: the beliefs it rested on are left alone
		}
		out = append(out, revision.Surprise{DecisionID: d.ID, RestsOn: d.Beliefs})
		exps[d.ID] = exp
	}
	return out, exps, nil
}

// revisionInputs loads the rested-on beliefs, the live contradicts edges
// touching them, and the beliefs on the other end of those edges.
func revisionInputs(ctx context.Context, conn *store.Conn, surprises []revision.Surprise) (revisionInputsWithRestsOn, map[string]domain.Claim, error) {
	in := revisionInputsWithRestsOn{
		Inputs:  revision.Inputs{Surprises: surprises, Beliefs: map[string]revision.Belief{}, Contradicts: map[string][]string{}},
		restsOn: map[string][]string{},
	}
	var ids []string
	seen := map[string]bool{}
	for _, s := range surprises {
		in.restsOn[s.DecisionID] = s.RestsOn
		for _, id := range s.RestsOn {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	rels, err := conn.Relationships.ListByClaimIDs(ctx, ids)
	if err != nil {
		return in, nil, fmt.Errorf("revise: list relationships: %w", err)
	}
	all := append([]string(nil), ids...)
	for _, r := range rels {
		if r.Type != domain.RelationshipTypeContradicts {
			continue
		}
		in.Contradicts[r.FromClaimID] = append(in.Contradicts[r.FromClaimID], r.ToClaimID)
		in.Contradicts[r.ToClaimID] = append(in.Contradicts[r.ToClaimID], r.FromClaimID)
		for _, id := range []string{r.FromClaimID, r.ToClaimID} {
			if !seen[id] {
				seen[id] = true
				all = append(all, id)
			}
		}
	}
	found, err := conn.Claims.ListByIDs(ctx, all)
	if err != nil {
		return in, nil, fmt.Errorf("revise: load beliefs: %w", err)
	}
	claims := make(map[string]domain.Claim, len(found))
	for _, c := range found {
		claims[c.ID] = c
		b := revision.Belief{
			ID: c.ID, Status: c.Status, Current: c.IsCurrent(),
			Promoted:  c.Lifecycle == domain.ClaimLifecyclePromoted,
			CreatedAt: c.CreatedAt, ValidFrom: c.ValidFrom,
		}
		if c.Status == domain.ClaimStatusContested && seenAsRestedOn(c.ID, in.restsOn) {
			flagged, err := flaggedBySurprise(ctx, conn, c.ID)
			if err != nil {
				return in, nil, err
			}
			b.FlaggedBySurprise = flagged
		}
		in.Beliefs[c.ID] = b
	}
	return in, claims, nil
}

func seenAsRestedOn(id string, restsOn map[string][]string) bool {
	for _, ids := range restsOn {
		for _, r := range ids {
			if r == id {
				return true
			}
		}
	}
	return false
}

// flaggedBySurprise reports whether the claim's latest status transition is a
// surprise flag.
func flaggedBySurprise(ctx context.Context, conn *store.Conn, claimID string) (bool, error) {
	hist, err := conn.Claims.ListStatusHistoryByClaimID(ctx, claimID)
	if err != nil {
		return false, fmt.Errorf("revise: status history of %s: %w", claimID, err)
	}
	if len(hist) == 0 {
		return false, nil
	}
	last := hist[len(hist)-1]
	return IsSurpriseFlag(last), nil
}

// IsSurpriseFlag reports whether a status transition is a flag set by
// surprise-triggered revision. `recompute-contested` uses it to leave such a
// flag alone: its heuristic does not produce it, so it would otherwise clear
// it as stale.
func IsSurpriseFlag(t domain.ClaimStatusTransition) bool {
	return t.ToStatus == domain.ClaimStatusContested && strings.HasPrefix(t.Reason, revision.ReasonPrefix)
}

// applyRevision writes one step through the store's audited paths: a status
// change records a claim_status_history row, a supersession a validity-history
// row, each with a reason that names the decisions behind it.
func applyRevision(ctx context.Context, conn *store.Conn, step revision.Step, claims map[string]domain.Claim, explainedBy map[string]string, now time.Time, actor string) error {
	c, ok := claims[step.ClaimID]
	if !ok {
		return fmt.Errorf("revise: belief %s vanished during revision", step.ClaimID)
	}
	decisions := strings.Join(step.DecisionIDs, ",")
	switch step.Kind {
	case revision.StepFlag:
		c.Status = domain.ClaimStatusContested
		reason := fmt.Sprintf("%s flagged: rested on by surprised decision(s) %s", revision.ReasonPrefix, decisions)
		if err := upsertClaims(ctx, conn, []domain.Claim{c}, ClaimReason{Reason: reason, ChangedBy: actor}); err != nil {
			return fmt.Errorf("revise: flag %s: %w", c.ID, err)
		}
	case revision.StepClear:
		c.Status = domain.ClaimStatusActive
		var by []string
		for _, d := range step.DecisionIDs {
			if e, ok := explainedBy[d]; ok {
				by = append(by, e)
			}
		}
		reason := fmt.Sprintf("%s cleared: surprise of decision(s) %s explained by superseding %s",
			revision.ReasonPrefix, decisions, strings.Join(by, ","))
		if err := upsertClaims(ctx, conn, []domain.Claim{c}, ClaimReason{Reason: reason, ChangedBy: actor}); err != nil {
			return fmt.Errorf("revise: clear %s: %w", c.ID, err)
		}
	case revision.StepSupersede:
		validTo := supersessionInstant(c, claims[step.By], now)
		note := domain.ChangeNote{
			Reason:    fmt.Sprintf("%s superseded by %s after surprised decision(s) %s", revision.ReasonPrefix, step.By, decisions),
			ChangedBy: actor,
		}
		if err := conn.Claims.SetValidity(ctx, c.ID, validTo, note); err != nil {
			return fmt.Errorf("revise: supersede %s: %w", c.ID, err)
		}
	default:
		return fmt.Errorf("revise: unknown step kind %q", step.Kind)
	}
	return nil
}

// supersessionInstant is when the superseded belief stopped being true: the
// successor's valid-from when it is known and lies between the old belief's
// valid-from and now, otherwise now. The same cutoff `mnemos resolve
// --supersedes` uses.
func supersessionInstant(old, successor domain.Claim, now time.Time) time.Time {
	from := successor.ValidFrom
	if from.IsZero() || from.After(now) || !from.After(old.ValidFrom) {
		return now
	}
	return from
}
