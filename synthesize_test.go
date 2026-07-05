package mnemos

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// seedActionOutcomes seeds n actions of the same (subject, kind) each with a
// success outcome — a corroborating cluster synthesize can turn into a lesson.
func seedActionOutcomes(t *testing.T, m *memory, n int) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		aid := fmt.Sprintf("act%d", i)
		if err := m.conn.Actions.Append(ctx, domain.Action{
			ID: aid, Kind: domain.ActionKindRollback, Subject: "api", Actor: "grace", At: now,
		}); err != nil {
			t.Fatalf("append action: %v", err)
		}
		if err := m.conn.Outcomes.Append(ctx, domain.Outcome{
			ID: fmt.Sprintf("out%d", i), ActionID: aid, Result: domain.OutcomeResultSuccess, ObservedAt: now,
		}); err != nil {
			t.Fatalf("append outcome: %v", err)
		}
	}
}

// TestSynthesize_DerivesSkills verifies the public Synthesize derives lessons from
// corroborating actions-with-outcomes and playbooks from those lessons.
func TestSynthesize_DerivesSkills(t *testing.T) {
	m := calibMem(t)
	seedActionOutcomes(t, m, 3)

	res, err := m.Synthesize(context.Background())
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if res.LessonsDerived < 1 {
		t.Errorf("expected >= 1 lesson derived, got %d", res.LessonsDerived)
	}
	if res.PlaybooksDerived < 1 {
		t.Errorf("expected >= 1 playbook derived, got %d", res.PlaybooksDerived)
	}
}

// TestConsolidate_SynthesizeAutoTrigger verifies the sleep pass auto-derives skills.
func TestConsolidate_SynthesizeAutoTrigger(t *testing.T) {
	m := calibMem(t)
	seedActionOutcomes(t, m, 3)

	res, err := m.Consolidate(context.Background(), ConsolidateOptions{Synthesize: true})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.LessonsSynthesized < 1 {
		t.Errorf("auto-trigger should derive lessons; got %d", res.LessonsSynthesized)
	}
	if res.PlaybooksSynthesized < 1 {
		t.Errorf("auto-trigger should derive playbooks; got %d", res.PlaybooksSynthesized)
	}
}
