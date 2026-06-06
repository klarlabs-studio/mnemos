package memory

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// DecisionRepository is the in-memory implementation of
// [ports.DecisionRepository].
type DecisionRepository struct {
	state *state
}

// Append upserts a decision and seeds its belief link rows.
func (r DecisionRepository) Append(_ context.Context, decision domain.Decision) error {
	if err := decision.Validate(); err != nil {
		return fmt.Errorf("invalid decision: %w", err)
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	createdAt := decision.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	stored := storedDecision{
		ID:              decision.ID,
		Statement:       decision.Statement,
		Plan:            decision.Plan,
		Reasoning:       decision.Reasoning,
		RiskLevel:       decision.RiskLevel,
		Alternatives:    append([]string(nil), decision.Alternatives...),
		OutcomeID:       decision.OutcomeID,
		Scope:           decision.Scope,
		ChosenAt:        decision.ChosenAt.UTC(),
		CreatedBy:       actorOr(decision.CreatedBy),
		CreatedAt:       createdAt.UTC(),
		RefutedBeliefs:  append([]string(nil), decision.RefutedBeliefs...),
		FailedOutcomeID: decision.FailedOutcomeID,
	}
	if _, exists := r.state.decisions[decision.ID]; !exists {
		r.state.decisionOrder = append(r.state.decisionOrder, decision.ID)
	}
	r.state.decisions[decision.ID] = stored
	if r.state.decisionBeliefs[decision.ID] == nil {
		r.state.decisionBeliefs[decision.ID] = map[string]struct{}{}
	}
	for _, cid := range decision.Beliefs {
		r.state.decisionBeliefs[decision.ID][cid] = struct{}{}
	}
	return nil
}

// GetByID returns the decision plus its beliefs.
func (r DecisionRepository) GetByID(_ context.Context, id string) (domain.Decision, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	stored, ok := r.state.decisions[id]
	if !ok {
		return domain.Decision{}, fmt.Errorf("decision %s not found", id)
	}
	d := stored.toDomain()
	d.Beliefs = r.beliefsForLocked(id)
	return d, nil
}

// ListAll returns every decision newest-first by chosen_at.
func (r DecisionRepository) ListAll(_ context.Context) ([]domain.Decision, error) {
	return r.listFiltered(func(_ storedDecision) bool { return true }), nil
}

// ListByRiskLevel returns decisions matching the given risk level.
func (r DecisionRepository) ListByRiskLevel(_ context.Context, level string) ([]domain.Decision, error) {
	return r.listFiltered(func(s storedDecision) bool { return string(s.RiskLevel) == level }), nil
}

// AttachOutcome wires an outcome id onto an existing decision.
func (r DecisionRepository) AttachOutcome(_ context.Context, decisionID, outcomeID string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	d, ok := r.state.decisions[decisionID]
	if !ok {
		return fmt.Errorf("decision %s not found", decisionID)
	}
	d.OutcomeID = outcomeID
	r.state.decisions[decisionID] = d
	return nil
}

// CountAll returns the total number of decisions stored.
func (r DecisionRepository) CountAll(_ context.Context) (int64, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return int64(len(r.state.decisions)), nil
}

// DeleteAll wipes decisions + decision_beliefs.
func (r DecisionRepository) DeleteAll(_ context.Context) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.decisions = map[string]storedDecision{}
	r.state.decisionOrder = nil
	r.state.decisionBeliefs = map[string]map[string]struct{}{}
	return nil
}

// AppendBeliefs is idempotent on (decision_id, claim_id).
func (r DecisionRepository) AppendBeliefs(_ context.Context, decisionID string, claimIDs []string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.decisionBeliefs[decisionID] == nil {
		r.state.decisionBeliefs[decisionID] = map[string]struct{}{}
	}
	for _, cid := range claimIDs {
		r.state.decisionBeliefs[decisionID][cid] = struct{}{}
	}
	return nil
}

// ListBeliefs returns the claim ids that backed the decision.
func (r DecisionRepository) ListBeliefs(_ context.Context, decisionID string) ([]string, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.beliefsForLocked(decisionID), nil
}

func (r DecisionRepository) listFiltered(pred func(storedDecision) bool) []domain.Decision {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Decision, 0, len(r.state.decisions))
	for _, id := range r.state.decisionOrder {
		s, ok := r.state.decisions[id]
		if !ok || !pred(s) {
			continue
		}
		d := s.toDomain()
		d.Beliefs = r.beliefsForLocked(id)
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ChosenAt.After(out[j].ChosenAt) })
	return out
}

func (r DecisionRepository) beliefsForLocked(decisionID string) []string {
	set := r.state.decisionBeliefs[decisionID]
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for cid := range set {
		out = append(out, cid)
	}
	sort.Strings(out)
	return out
}

type storedDecision struct {
	ID              string
	Statement       string
	Plan            string
	Reasoning       string
	RiskLevel       domain.RiskLevel
	Alternatives    []string
	OutcomeID       string
	Scope           domain.Scope
	ChosenAt        time.Time
	CreatedBy       string
	CreatedAt       time.Time
	RefutedBeliefs  []string
	FailedOutcomeID string
}

func (s storedDecision) toDomain() domain.Decision {
	return domain.Decision{
		ID:              s.ID,
		Statement:       s.Statement,
		Plan:            s.Plan,
		Reasoning:       s.Reasoning,
		RiskLevel:       s.RiskLevel,
		Alternatives:    append([]string(nil), s.Alternatives...),
		OutcomeID:       s.OutcomeID,
		Scope:           s.Scope,
		ChosenAt:        s.ChosenAt,
		CreatedBy:       s.CreatedBy,
		CreatedAt:       s.CreatedAt,
		RefutedBeliefs:  append([]string(nil), s.RefutedBeliefs...),
		FailedOutcomeID: s.FailedOutcomeID,
	}
}
