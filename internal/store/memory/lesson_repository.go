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

// LessonRepository is the in-memory implementation of
// [ports.LessonRepository]. Append upserts by id so re-running
// synthesis ratchets confidence forward without churning identity.
type LessonRepository struct {
	state *state
}

// Append upserts a lesson and seeds its evidence rows. Snapshots the
// prior shape into lessonVersions before overwrite.
func (r LessonRepository) Append(_ context.Context, lesson domain.Lesson) error {
	if err := lesson.Validate(); err != nil {
		return fmt.Errorf("invalid lesson: %w", err)
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if prev, ok := r.state.lessons[lesson.ID]; ok {
		domainPrev := prev.toDomain()
		domainPrev.Evidence = r.evidenceForLocked(lesson.ID)
		if payload, err := json.Marshal(domainPrev); err == nil {
			r.state.lessonVersions[lesson.ID] = append(r.state.lessonVersions[lesson.ID], storedEntityVersion{
				PayloadJSON: string(payload),
				ValidFrom:   domainPrev.DerivedAt,
				ValidTo:     time.Now().UTC(),
			})
		}
	}
	source := lesson.Source
	if source == "" {
		source = "synthesize"
	}
	stored := storedLesson{
		ID:           lesson.ID,
		Statement:    lesson.Statement,
		Scope:        lesson.Scope,
		Trigger:      lesson.Trigger,
		Kind:         lesson.Kind,
		Confidence:   lesson.Confidence,
		Polarity:     lesson.Polarity,
		DerivedAt:    lesson.DerivedAt.UTC(),
		LastVerified: lesson.LastVerified.UTC(),
		Source:       source,
		CreatedBy:    actorOr(lesson.CreatedBy),
	}
	if _, exists := r.state.lessons[lesson.ID]; !exists {
		r.state.lessonOrder = append(r.state.lessonOrder, lesson.ID)
	}
	r.state.lessons[lesson.ID] = stored
	if r.state.lessonEvidence[lesson.ID] == nil {
		r.state.lessonEvidence[lesson.ID] = map[string]struct{}{}
	}
	for _, aid := range lesson.Evidence {
		r.state.lessonEvidence[lesson.ID][aid] = struct{}{}
	}
	return nil
}

// GetByID returns the lesson with the given id plus its evidence.
func (r LessonRepository) GetByID(_ context.Context, id string) (domain.Lesson, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	stored, ok := r.state.lessons[id]
	if !ok {
		return domain.Lesson{}, fmt.Errorf("lesson %s not found", id)
	}
	l := stored.toDomain()
	l.Evidence = r.evidenceForLocked(id)
	return l, nil
}

// ListByService returns lessons scoped to the given service.
func (r LessonRepository) ListByService(_ context.Context, service string) ([]domain.Lesson, error) {
	return r.listFiltered(func(s storedLesson) bool { return s.Scope.Service == service }), nil
}

// ListByTrigger returns lessons matching a trigger label.
func (r LessonRepository) ListByTrigger(_ context.Context, trigger string) ([]domain.Lesson, error) {
	return r.listFiltered(func(s storedLesson) bool { return s.Trigger == trigger }), nil
}

// ListAll returns every lesson sorted by confidence desc, derived_at desc.
func (r LessonRepository) ListAll(_ context.Context) ([]domain.Lesson, error) {
	return r.listFiltered(func(_ storedLesson) bool { return true }), nil
}

// CountAll returns the total number of lessons stored.
func (r LessonRepository) CountAll(_ context.Context) (int64, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return int64(len(r.state.lessons)), nil
}

// DeleteAll wipes every lesson and its evidence rows.
func (r LessonRepository) DeleteAll(_ context.Context) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.lessons = map[string]storedLesson{}
	r.state.lessonOrder = nil
	r.state.lessonEvidence = map[string]map[string]struct{}{}
	return nil
}

// AppendEvidence is idempotent on (lesson_id, action_id).
func (r LessonRepository) AppendEvidence(_ context.Context, lessonID string, actionIDs []string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.lessonEvidence[lessonID] == nil {
		r.state.lessonEvidence[lessonID] = map[string]struct{}{}
	}
	for _, aid := range actionIDs {
		r.state.lessonEvidence[lessonID][aid] = struct{}{}
	}
	return nil
}

// ListEvidence returns the action ids backing a given lesson.
func (r LessonRepository) ListEvidence(_ context.Context, lessonID string) ([]string, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.evidenceForLocked(lessonID), nil
}

// ListVersions returns snapshots of prior lesson states newest first.
func (r LessonRepository) ListVersions(_ context.Context, lessonID string) ([]ports.EntityVersion, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	versions := r.state.lessonVersions[lessonID]
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

func (r LessonRepository) listFiltered(pred func(storedLesson) bool) []domain.Lesson {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Lesson, 0, len(r.state.lessons))
	for _, id := range r.state.lessonOrder {
		s, ok := r.state.lessons[id]
		if !ok || !pred(s) {
			continue
		}
		l := s.toDomain()
		l.Evidence = r.evidenceForLocked(id)
		out = append(out, l)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].DerivedAt.After(out[j].DerivedAt)
	})
	return out
}

func (r LessonRepository) evidenceForLocked(lessonID string) []string {
	set := r.state.lessonEvidence[lessonID]
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for aid := range set {
		out = append(out, aid)
	}
	sort.Strings(out)
	return out
}

type storedLesson struct {
	ID           string
	Statement    string
	Scope        domain.LessonScope
	Trigger      string
	Kind         string
	Confidence   float64
	Polarity     domain.LessonPolarity
	DerivedAt    time.Time
	LastVerified time.Time
	Source       string
	CreatedBy    string
}

func (s storedLesson) toDomain() domain.Lesson {
	return domain.Lesson{
		ID:           s.ID,
		Statement:    s.Statement,
		Scope:        s.Scope,
		Trigger:      s.Trigger,
		Kind:         s.Kind,
		Confidence:   s.Confidence,
		Polarity:     s.Polarity,
		DerivedAt:    s.DerivedAt,
		LastVerified: s.LastVerified,
		Source:       s.Source,
		CreatedBy:    s.CreatedBy,
	}
}
