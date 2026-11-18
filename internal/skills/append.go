package skills

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// MaxBatch is the largest number of records one append call accepts.
const MaxBatch = 1000

// ErrInvalidRecord is returned when an appended record fails validation. The
// wrapped message names the offending element, so transports can answer 400
// / InvalidArgument with something the caller can act on rather than an
// opaque write failure from inside the governed kernel.
var ErrInvalidRecord = errors.New("invalid record")

// SchemaWriter persists one schema (lesson) through the governed write path:
// govwrite.Writer.Lesson on every daemon surface.
type SchemaWriter func(ctx context.Context, lesson domain.Lesson) (string, error)

// ReflexWriter persists one reflex (playbook) through the governed write path:
// govwrite.Writer.Playbook on every daemon surface.
type ReflexWriter func(ctx context.Context, playbook domain.Playbook) (string, error)

// AppendSchemas validates the whole batch, then writes each schema. Defaults:
// DerivedAt is now when zero and CreatedBy is actor when empty. Nothing is written when any element is invalid. Returns the count written.
func AppendSchemas(ctx context.Context, write SchemaWriter, actor string, lessons []domain.Lesson) (int, error) {
	if err := checkBatch("schemas", len(lessons)); err != nil {
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
// same defaults and all-or-nothing validation as [AppendSchemas].
func AppendReflexes(ctx context.Context, write ReflexWriter, actor string, playbooks []domain.Playbook) (int, error) {
	if err := checkBatch("reflexes", len(playbooks)); err != nil {
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
		prepared[i] = p
	}
	for _, p := range prepared {
		if _, err := write(ctx, p); err != nil {
			return 0, fmt.Errorf("append reflex %s: %w", p.ID, err)
		}
	}
	return len(prepared), nil
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
