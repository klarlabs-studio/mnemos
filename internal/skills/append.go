package skills

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// MaxBatch is the largest number of records one append call accepts.
const MaxBatch = 1000

// ErrInvalidRecord is returned when an appended record fails validation or
// references a record that does not exist. The wrapped message names the
// offending element, so transports can answer 400 / InvalidArgument with
// something the caller can act on rather than an opaque write failure (a
// foreign-key violation) from inside the governed kernel.
var ErrInvalidRecord = errors.New("invalid record")

// SchemaWriter persists one schema (lesson) through the governed write path:
// govwrite.Writer.Lesson on every daemon surface.
type SchemaWriter func(ctx context.Context, lesson domain.Lesson) (string, error)

// ReflexWriter persists one reflex (playbook) through the governed write path:
// govwrite.Writer.Playbook on every daemon surface.
type ReflexWriter func(ctx context.Context, playbook domain.Playbook) (string, error)

// AppendSchemas validates the whole batch, then writes each schema. Defaults:
// DerivedAt is now when zero and CreatedBy is actor when empty. Every evidence
// id must name a stored action. Nothing is written when any element is
// invalid. Returns the count written.
func AppendSchemas(ctx context.Context, actions ports.ActionRepository, write SchemaWriter, actor string, lessons []domain.Lesson) (int, error) {
	if err := checkBatch("schemas", len(lessons)); err != nil {
		return 0, err
	}
	if actions == nil {
		return 0, fmt.Errorf("append schemas: action store: %w", ErrUnsupported)
	}
	known, err := actionIDs(ctx, actions)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	prepared := make([]domain.Lesson, len(lessons))
	for i, l := range lessons {
		if l.DerivedAt.IsZero() {
			l.DerivedAt = now
		}
		if strings.TrimSpace(l.CreatedBy) == "" {
			l.CreatedBy = actor
		}
		if err := l.Validate(); err != nil {
			return 0, fmt.Errorf("schemas[%d]: %v: %w", i, err, ErrInvalidRecord)
		}
		for _, id := range l.Evidence {
			if !known[id] {
				return 0, fmt.Errorf("schemas[%d]: evidence action %q does not exist: %w", i, id, ErrInvalidRecord)
			}
		}
		prepared[i] = l
	}
	for _, l := range prepared {
		if _, err := write(ctx, l); err != nil {
			return 0, fmt.Errorf("append schema %s: %w", l.ID, err)
		}
	}
	return len(prepared), nil
}

// AppendReflexes validates the whole batch, then writes each reflex, with the
// same defaults and all-or-nothing validation as [AppendSchemas]. Every
// derived-from id must name a stored schema.
func AppendReflexes(ctx context.Context, schemas ports.LessonRepository, write ReflexWriter, actor string, playbooks []domain.Playbook) (int, error) {
	if err := checkBatch("reflexes", len(playbooks)); err != nil {
		return 0, err
	}
	if schemas == nil {
		return 0, fmt.Errorf("append reflexes: schema store: %w", ErrUnsupported)
	}
	known, err := schemaIDs(ctx, schemas)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	prepared := make([]domain.Playbook, len(playbooks))
	for i, p := range playbooks {
		if p.DerivedAt.IsZero() {
			p.DerivedAt = now
		}
		if strings.TrimSpace(p.CreatedBy) == "" {
			p.CreatedBy = actor
		}
		if err := p.Validate(); err != nil {
			return 0, fmt.Errorf("reflexes[%d]: %v: %w", i, err, ErrInvalidRecord)
		}
		for _, id := range p.DerivedFromLessons {
			if !known[id] {
				return 0, fmt.Errorf("reflexes[%d]: derived-from schema %q does not exist: %w", i, id, ErrInvalidRecord)
			}
		}
		prepared[i] = p
	}
	for _, p := range prepared {
		if _, err := write(ctx, p); err != nil {
			return 0, fmt.Errorf("append reflex %s: %w", p.ID, err)
		}
	}
	return len(prepared), nil
}

func actionIDs(ctx context.Context, repo ports.ActionRepository) (map[string]bool, error) {
	all, err := repo.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve evidence actions: %w", err)
	}
	out := make(map[string]bool, len(all))
	for _, a := range all {
		out[a.ID] = true
	}
	return out, nil
}

func schemaIDs(ctx context.Context, repo ports.LessonRepository) (map[string]bool, error) {
	all, err := repo.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve derived-from schemas: %w", err)
	}
	out := make(map[string]bool, len(all))
	for _, l := range all {
		out[l.ID] = true
	}
	return out, nil
}

func checkBatch(what string, n int) error {
	if n == 0 {
		return fmt.Errorf("%s array is empty: %w", what, ErrInvalidRecord)
	}
	if n > MaxBatch {
		return fmt.Errorf("%s batch size %d exceeds max %d: %w", what, n, MaxBatch, ErrInvalidRecord)
	}
	return nil
}
