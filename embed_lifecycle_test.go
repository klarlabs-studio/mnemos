package mnemos_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/providers"
)

// blockingEmbedder holds each Embed call until released, so a test can be
// certain a background embed is genuinely in flight when Close runs.
type blockingEmbedder struct {
	entered  chan struct{}
	release  chan struct{}
	returned atomic.Int32
	notified atomic.Bool
}

func newBlockingEmbedder() *blockingEmbedder {
	return &blockingEmbedder{
		entered: make(chan struct{}, 16),
		release: make(chan struct{}),
	}
}

func (b *blockingEmbedder) Embed(ctx context.Context, in providers.EmbedInput) (providers.EmbedOutput, error) {
	if !b.notified.Swap(true) {
		close(b.entered)
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		b.returned.Add(1)
		return providers.EmbedOutput{}, ctx.Err()
	case <-time.After(10 * time.Second):
	}
	b.returned.Add(1)
	vectors := make([][]float32, len(in.Texts))
	for i := range vectors {
		vectors[i] = []float32{0.1, 0.2, 0.3}
	}
	return providers.EmbedOutput{Vectors: vectors, Model: "test-embed"}, nil
}

type stubTextGenerator struct{}

func (stubTextGenerator) GenerateText(ctx context.Context, in providers.GenerateTextInput) (providers.GenerateTextOutput, error) {
	return providers.GenerateTextOutput{Content: "[]", Model: "test-llm"}, nil
}

// Close must not tear the connection out from under an in-flight background
// embed. embedAsync re-read m.conn inside its goroutine while Close nil'd that
// field with no synchronisation — a data race, and a nil dereference that
// panics the process. On a long-lived `mnemos mcp` server (remember_episode
// does `defer mem.Close()` and returns while the 30s embed round-trip is still
// running) that panic takes down every other in-flight tool call too.
//
// Run with -race to catch the race as well as the panic.
func TestClose_WaitsForInFlightEmbed(t *testing.T) {
	clearMnemosEnv(t)

	embedder := newBlockingEmbedder()
	mem, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace=embed_lifecycle_close"),
		mnemos.WithSharedProvider(stubTextGenerator{}, embedder),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	// RememberEvent is one of the three paths that fire embedAsync, and the one
	// the remember_episode MCP tool uses — the handler that does
	// `defer mem.Close()` and returns mid-embed.
	if err := mem.RememberEvent(ctx, mnemos.Event{
		At:      time.Now(),
		Content: "Closing a memory must not panic an in-flight embedding.",
		Type:    "note",
	}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}

	// Assert the Close behaviour only once an embed is genuinely in flight —
	// otherwise the test would pass without ever exercising the race.
	select {
	case <-embedder.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no background embed was triggered; this test would not exercise the race")
	}

	done := make(chan error, 1)
	go func() { done <- mem.Close() }()

	select {
	case err := <-done:
		// Close cancelled the embed context and drained, rather than blocking
		// for the embedder's full duration.
		if err != nil && !strings.Contains(err.Error(), "closed") {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(8 * time.Second):
		close(embedder.release)
		t.Fatal("Close did not return; the drain is not cancelling in-flight embeds")
	}

	if embedder.returned.Load() == 0 {
		t.Error("Close returned while the embed goroutine was still running — it was not drained")
	}
}

// A second Close must stay safe: handlers routinely pair an explicit Close with
// a deferred one.
func TestClose_IsIdempotent(t *testing.T) {
	clearMnemosEnv(t)

	mem, err := mnemos.New(mnemos.WithStorage("memory://?namespace=embed_lifecycle_double"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := mem.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := mem.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
