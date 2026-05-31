package mnemos_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/felixgeelhaar/mnemos"
	"github.com/felixgeelhaar/mnemos/providers"

	// Storage providers used by the tests; blank-imported here for
	// scheme registration.
	_ "github.com/felixgeelhaar/mnemos/internal/store/memory"
)

// TestNew_PassiveMode_ZeroConfig verifies a fresh consumer can construct
// a Memory with no environment variables and no options beyond storage.
func TestNew_PassiveMode_ZeroConfig(t *testing.T) {
	t.Parallel()
	clearMnemosEnv(t)

	mem, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace=mnemos_passive_test"),
		mnemos.WithPassiveMode(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = mem.Close() }()

	ctx := context.Background()
	if err := mem.Remember(ctx, mnemos.Item{
		Type:    "fact",
		Content: "The user prefers Go programming language for backend work.",
		Source:  "test",
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	// Token-overlap ranking needs shared tokens between query + claim.
	// We deliberately overlap on "user" / "Go" / "language".
	results, err := mem.Recall(ctx, mnemos.Query{Text: "user Go language"})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("Recall returned 0 results; expected at least 1")
	}
	if !strings.Contains(strings.ToLower(results[0].Text), "go") {
		t.Errorf("top result text = %q; expected to mention Go", results[0].Text)
	}
}

// TestRememberEvent_Timeline verifies temporal recall works without
// Chronos wired up (raw event-store path). Chronos integration lands in
// task 12; this test pins the storage-side contract.
func TestRememberEvent_Timeline(t *testing.T) {
	t.Parallel()
	clearMnemosEnv(t)

	mem, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace=mnemos_timeline_test"),
		mnemos.WithPassiveMode(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = mem.Close() }()

	ctx := context.Background()
	base := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)

	for i, content := range []string{"deploy v1", "incident detected", "rollback to v0"} {
		if err := mem.RememberEvent(ctx, mnemos.Event{
			At:      base.Add(time.Duration(i) * time.Hour),
			Type:    "deployment",
			Content: content,
			RunID:   "timeline-test",
		}); err != nil {
			t.Fatalf("RememberEvent[%d]: %v", i, err)
		}
	}

	events, err := mem.Timeline(ctx, mnemos.TimelineQuery{RunID: "timeline-test"})
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("Timeline returned %d events; want 3", len(events))
	}
	for i := 1; i < len(events); i++ {
		if events[i].At.Before(events[i-1].At) {
			t.Errorf("events out of order at index %d", i)
		}
	}
	if events[0].Type != "deployment" {
		t.Errorf("Type = %q, want deployment", events[0].Type)
	}
}

// TestNew_SharedProvider verifies a [providers.TextGenerator] supplied
// by the caller is routed through the extractor. We use a stub generator
// that records its call count and returns a deterministic shape; the
// test asserts the extractor invoked it.
func TestNew_SharedProvider(t *testing.T) {
	t.Parallel()
	clearMnemosEnv(t)

	tg := &recordingTextGen{
		response: `[
			{"text": "the user prefers Go", "type": "fact", "confidence": 0.9}
		]`,
	}

	mem, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace=mnemos_shared_test"),
		mnemos.WithSharedProvider(tg, nil),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = mem.Close() }()

	// Unique content keeps the on-disk LLM extraction cache from
	// short-circuiting the test on repeat runs.
	uniq := time.Now().UnixNano()
	ctx := context.Background()
	if err := mem.Remember(ctx, mnemos.Item{
		Type:    "fact",
		Content: fmt.Sprintf("Shared-provider test %d: I work in Go.", uniq),
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if tg.calls == 0 {
		t.Error("shared TextGenerator was never invoked; extractor likely fell back to rules silently")
	}
}

type recordingTextGen struct {
	calls    int
	response string
}

func (g *recordingTextGen) GenerateText(
	_ context.Context,
	_ providers.GenerateTextInput,
) (providers.GenerateTextOutput, error) {
	g.calls++
	return providers.GenerateTextOutput{
		Content:      g.response,
		Model:        "stub-v1",
		InputTokens:  10,
		OutputTokens: 20,
	}, nil
}

// clearMnemosEnv ensures Mnemos env vars don't leak between tests.
func clearMnemosEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"MNEMOS_DB_URL",
		"MNEMOS_LLM_PROVIDER",
		"MNEMOS_LLM_API_KEY",
		"MNEMOS_LLM_MODEL",
		"MNEMOS_LLM_BASE_URL",
		"MNEMOS_EMBED_PROVIDER",
		"MNEMOS_EMBED_API_KEY",
		"MNEMOS_EMBED_MODEL",
		"MNEMOS_EMBED_BASE_URL",
		"MNEMOS_USER_ID",
	} {
		_ = os.Unsetenv(k)
	}
}
