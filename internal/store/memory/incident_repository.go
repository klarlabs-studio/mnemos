package memory

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// IncidentRepository is the in-memory implementation of
// [ports.IncidentRepository]. It is backed by the shared [state] pointer
// so writes are visible across all repositories opened from the same Conn.
type IncidentRepository struct {
	state *state
}

// Upsert stores or replaces the incident by ID.
func (r IncidentRepository) Upsert(_ context.Context, incident domain.Incident) error {
	if err := incident.Validate(); err != nil {
		return fmt.Errorf("invalid incident: %w", err)
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if _, exists := r.state.incidents[incident.ID]; !exists {
		r.state.incidentOrder = append(r.state.incidentOrder, incident.ID)
	}
	r.state.incidents[incident.ID] = incident
	return nil
}

// GetByID returns the incident with the given ID.
func (r IncidentRepository) GetByID(_ context.Context, id string) (domain.Incident, bool, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	inc, ok := r.state.incidents[id]
	return inc, ok, nil
}

// ListAll returns every incident ordered by opened_at descending.
func (r IncidentRepository) ListAll(_ context.Context) ([]domain.Incident, error) {
	return r.listFiltered(func(_ domain.Incident) bool { return true }), nil
}

// ListBySeverity returns incidents matching the given severity.
func (r IncidentRepository) ListBySeverity(_ context.Context, severity domain.IncidentSeverity) ([]domain.Incident, error) {
	return r.listFiltered(func(i domain.Incident) bool { return i.Severity == severity }), nil
}

// ListByStatus returns incidents matching the given status.
func (r IncidentRepository) ListByStatus(_ context.Context, status domain.IncidentStatus) ([]domain.Incident, error) {
	return r.listFiltered(func(i domain.Incident) bool { return i.Status == status }), nil
}

// Resolve sets status to resolved and stamps resolved_at. Idempotent.
func (r IncidentRepository) Resolve(_ context.Context, id string, resolvedAt time.Time) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	inc, ok := r.state.incidents[id]
	if !ok {
		return fmt.Errorf("incident %s not found", id)
	}
	inc.Status = domain.IncidentStatusResolved
	inc.ResolvedAt = resolvedAt.UTC()
	r.state.incidents[id] = inc
	return nil
}

// AttachDecision appends decisionID to the incident's DecisionIDs if absent.
func (r IncidentRepository) AttachDecision(_ context.Context, incidentID, decisionID string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	inc, ok := r.state.incidents[incidentID]
	if !ok {
		return fmt.Errorf("incident %s not found", incidentID)
	}
	for _, id := range inc.DecisionIDs {
		if id == decisionID {
			return nil
		}
	}
	inc.DecisionIDs = append(inc.DecisionIDs, decisionID)
	r.state.incidents[incidentID] = inc
	return nil
}

// AttachOutcome appends outcomeID to the incident's OutcomeIDs if absent.
func (r IncidentRepository) AttachOutcome(_ context.Context, incidentID, outcomeID string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	inc, ok := r.state.incidents[incidentID]
	if !ok {
		return fmt.Errorf("incident %s not found", incidentID)
	}
	for _, id := range inc.OutcomeIDs {
		if id == outcomeID {
			return nil
		}
	}
	inc.OutcomeIDs = append(inc.OutcomeIDs, outcomeID)
	r.state.incidents[incidentID] = inc
	return nil
}

// SetPlaybook records the synthesised playbook ID on the incident.
func (r IncidentRepository) SetPlaybook(_ context.Context, incidentID, playbookID string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	inc, ok := r.state.incidents[incidentID]
	if !ok {
		return fmt.Errorf("incident %s not found", incidentID)
	}
	inc.PlaybookID = playbookID
	r.state.incidents[incidentID] = inc
	return nil
}

// CountAll returns the total number of incidents.
func (r IncidentRepository) CountAll(_ context.Context) (int64, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return int64(len(r.state.incidents)), nil
}

// DeleteAll wipes all incidents.
func (r IncidentRepository) DeleteAll(_ context.Context) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.incidents = map[string]domain.Incident{}
	r.state.incidentOrder = nil
	return nil
}

func (r IncidentRepository) listFiltered(pred func(domain.Incident) bool) []domain.Incident {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Incident, 0, len(r.state.incidents))
	for _, id := range r.state.incidentOrder {
		inc, ok := r.state.incidents[id]
		if !ok || !pred(inc) {
			continue
		}
		out = append(out, inc)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].OpenedAt.After(out[j].OpenedAt)
	})
	return out
}
