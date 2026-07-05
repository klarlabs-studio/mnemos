package mnemos

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/synthesize"
)

// Synthesize implements [Memory.Synthesize]: derive lessons from
// actions-with-outcomes, then playbooks from lessons. Idempotent (upsert by
// cluster); a no-op on backends without the skill layer.
func (m *memory) Synthesize(ctx context.Context) (SynthesizeResult, error) {
	if m.conn.Actions == nil || m.conn.Outcomes == nil || m.conn.Lessons == nil || m.conn.Playbooks == nil {
		return SynthesizeResult{}, nil
	}
	lessons, err := synthesize.Synthesize(ctx, m.conn.Actions, m.conn.Outcomes, m.conn.Lessons, synthesize.Options{})
	if err != nil {
		return SynthesizeResult{}, fmt.Errorf("mnemos: Synthesize: lessons: %w", err)
	}
	playbooks, err := synthesize.Playbooks(ctx, m.conn.Lessons, m.conn.Playbooks, synthesize.PlaybookOptions{})
	if err != nil {
		return SynthesizeResult{}, fmt.Errorf("mnemos: Synthesize: playbooks: %w", err)
	}
	return SynthesizeResult{
		LessonsDerived:   lessons.LessonsEmitted,
		PlaybooksDerived: playbooks.PlaybooksEmitted,
	}, nil
}
