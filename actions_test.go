package mnemos_test

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// TestRecordAction_FeedsSynthesize verifies the action/outcome substrate closes
// the skill loop through the PUBLIC API: recording corroborating actions +
// success outcomes lets Synthesize derive a lesson and a playbook.
func TestRecordAction_FeedsSynthesize(t *testing.T) {
	mem := newReadMemory(t, "actions")
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		aid, err := mem.RecordAction(ctx, mnemos.ActionItem{
			Kind: "rollback", Subject: "api", Actor: "grace",
		})
		if err != nil {
			t.Fatalf("RecordAction %d: %v", i, err)
		}
		if aid == "" {
			t.Fatal("RecordAction returned an empty id")
		}
		if err := mem.RecordActionOutcome(ctx, mnemos.OutcomeItem{
			ActionID: aid, Result: "success",
		}); err != nil {
			t.Fatalf("RecordActionOutcome %d: %v", i, err)
		}
	}

	res, err := mem.Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if res.LessonsDerived < 1 {
		t.Errorf("recorded actions+outcomes should derive a lesson; got %d", res.LessonsDerived)
	}
	if res.PlaybooksDerived < 1 {
		t.Errorf("recorded actions+outcomes should derive a playbook; got %d", res.PlaybooksDerived)
	}
}

// TestRecordAction_Validation covers the required-field guards.
func TestRecordAction_Validation(t *testing.T) {
	mem := newReadMemory(t, "actions_validation")
	ctx := context.Background()

	if _, err := mem.RecordAction(ctx, mnemos.ActionItem{Subject: "api"}); err == nil {
		t.Error("RecordAction without a kind should error")
	}
	if _, err := mem.RecordAction(ctx, mnemos.ActionItem{Kind: "deploy"}); err == nil {
		t.Error("RecordAction without a subject should error")
	}
	if err := mem.RecordActionOutcome(ctx, mnemos.OutcomeItem{Result: "success"}); err == nil {
		t.Error("RecordActionOutcome without an ActionID should error")
	}
}
