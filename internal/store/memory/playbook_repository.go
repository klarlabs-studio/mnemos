package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// PlaybookRepository is the in-memory implementation of
// [ports.PlaybookRepository].
type PlaybookRepository struct {
	state *state
}

// Append upserts a playbook and seeds its lesson link rows. Snapshots
// the prior shape into playbookVersions before overwrite.
func (r PlaybookRepository) Append(_ context.Context, p domain.Playbook) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("invalid playbook: %w", err)
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if prev, ok := r.state.playbooks[p.ID]; ok {
		domainPrev := prev.toDomain()
		domainPrev.DerivedFromLessons = r.lessonsForLocked(p.ID)
		if payload, err := json.Marshal(domainPrev); err == nil {
			r.state.playbookVersions[p.ID] = append(r.state.playbookVersions[p.ID], storedEntityVersion{
				PayloadJSON: string(payload),
				ValidFrom:   domainPrev.DerivedAt,
				ValidTo:     time.Now().UTC(),
			})
		}
	}
	source := p.Source
	if source == "" {
		source = "synthesize"
	}
	stored := storedPlaybook{
		ID:           p.ID,
		Trigger:      p.Trigger,
		Statement:    p.Statement,
		Scope:        p.Scope,
		Steps:        append([]domain.PlaybookStep(nil), p.Steps...),
		Confidence:   p.Confidence,
		DerivedAt:    p.DerivedAt.UTC(),
		LastVerified: p.LastVerified.UTC(),
		Source:       source,
		CreatedBy:    actorOr(p.CreatedBy),
	}
	if _, exists := r.state.playbooks[p.ID]; !exists {
		r.state.playbookOrder = append(r.state.playbookOrder, p.ID)
	}
	r.state.playbooks[p.ID] = stored
	if r.state.playbookLessons[p.ID] == nil {
		r.state.playbookLessons[p.ID] = map[string]struct{}{}
	}
	for _, lid := range p.DerivedFromLessons {
		r.state.playbookLessons[p.ID][lid] = struct{}{}
	}
	return nil
}

// GetByID returns the playbook plus its lesson provenance.
func (r PlaybookRepository) GetByID(_ context.Context, id string) (domain.Playbook, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	stored, ok := r.state.playbooks[id]
	if !ok {
		return domain.Playbook{}, fmt.Errorf("playbook %s not found", id)
	}
	p := stored.toDomain()
	p.DerivedFromLessons = r.lessonsForLocked(id)
	return p, nil
}

// ListByTrigger returns playbooks matching a trigger label.
func (r PlaybookRepository) ListByTrigger(_ context.Context, trigger string) ([]domain.Playbook, error) {
	return r.listFiltered(func(p storedPlaybook) bool { return p.Trigger == trigger }), nil
}

// ListByService returns playbooks scoped to the given service.
func (r PlaybookRepository) ListByService(_ context.Context, service string) ([]domain.Playbook, error) {
	return r.listFiltered(func(p storedPlaybook) bool { return p.Scope.Service == service }), nil
}

// ListAll returns every playbook, highest confidence first.
func (r PlaybookRepository) ListAll(_ context.Context) ([]domain.Playbook, error) {
	return r.listFiltered(func(_ storedPlaybook) bool { return true }), nil
}

// CountAll returns the total number of playbooks stored.
func (r PlaybookRepository) CountAll(_ context.Context) (int64, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return int64(len(r.state.playbooks)), nil
}

// DeleteAll wipes playbooks + playbook_lessons.
func (r PlaybookRepository) DeleteAll(_ context.Context) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.playbooks = map[string]storedPlaybook{}
	r.state.playbookOrder = nil
	r.state.playbookLessons = map[string]map[string]struct{}{}
	return nil
}

// AppendLessons is idempotent on (playbook_id, lesson_id).
func (r PlaybookRepository) AppendLessons(_ context.Context, playbookID string, lessonIDs []string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.playbookLessons[playbookID] == nil {
		r.state.playbookLessons[playbookID] = map[string]struct{}{}
	}
	for _, lid := range lessonIDs {
		r.state.playbookLessons[playbookID][lid] = struct{}{}
	}
	return nil
}

// ListLessons returns the lesson ids that justified the playbook.
func (r PlaybookRepository) ListLessons(_ context.Context, playbookID string) ([]string, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.lessonsForLocked(playbookID), nil
}

// ListVersions returns snapshots of prior playbook states newest first.
func (r PlaybookRepository) ListVersions(_ context.Context, playbookID string) ([]ports.EntityVersion, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	versions := r.state.playbookVersions[playbookID]
	out := make([]ports.EntityVersion, 0, len(versions))
	for i := len(versions) - 1; i >= 0; i-- {
		v := versions[i]
		out = append(out, ports.EntityVersion{
			VersionID:   int64(i + 1),
			PayloadJSON: v.PayloadJSON,
			ValidFrom:   v.ValidFrom,
			ValidTo:     v.ValidTo,
		})
	}
	return out, nil
}

func (r PlaybookRepository) listFiltered(pred func(storedPlaybook) bool) []domain.Playbook {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Playbook, 0, len(r.state.playbooks))
	for _, id := range r.state.playbookOrder {
		s, ok := r.state.playbooks[id]
		if !ok || !pred(s) {
			continue
		}
		p := s.toDomain()
		p.DerivedFromLessons = r.lessonsForLocked(id)
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].DerivedAt.After(out[j].DerivedAt)
	})
	return out
}

func (r PlaybookRepository) lessonsForLocked(playbookID string) []string {
	set := r.state.playbookLessons[playbookID]
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for lid := range set {
		out = append(out, lid)
	}
	sort.Strings(out)
	return out
}

type storedPlaybook struct {
	ID           string
	Trigger      string
	Statement    string
	Scope        domain.LessonScope
	Steps        []domain.PlaybookStep
	Confidence   float64
	DerivedAt    time.Time
	LastVerified time.Time
	Source       string
	CreatedBy    string
}

func (s storedPlaybook) toDomain() domain.Playbook {
	return domain.Playbook{
		ID:           s.ID,
		Trigger:      s.Trigger,
		Statement:    s.Statement,
		Scope:        s.Scope,
		Steps:        append([]domain.PlaybookStep(nil), s.Steps...),
		Confidence:   s.Confidence,
		DerivedAt:    s.DerivedAt,
		LastVerified: s.LastVerified,
		Source:       s.Source,
		CreatedBy:    s.CreatedBy,
	}
}
