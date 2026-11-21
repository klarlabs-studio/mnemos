package mnemos_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/embedding"
	"go.klarlabs.de/mnemos/internal/store"
)

// A batch larger than the embed worker pool leaves a queue behind the
// in-flight embeds. Close used to wait a fixed 5s and then cancel whatever was
// still queued, so a process that wrote a large batch and exited stored the
// tail of it with no vector, silently (#460). A provider that is still making
// progress must be allowed to finish the whole queue.
func TestClose_DrainsQueuedEmbedsWhileProviderProgresses(t *testing.T) {
	clearMnemosEnv(t)
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "brain.db")
	// 4 concurrent slots x 300ms per embed: 80 events take ~6s, longer than
	// the old fixed 5s grace, while every single embed finishes well inside it.
	mem, err := mnemos.New(mnemos.WithStorage(dsn), mnemos.WithSharedProvider(nil, slowEmbedder{delay: 300 * time.Millisecond}))
	if err != nil {
		t.Fatal(err)
	}
	const events = 80
	for i := range events {
		if err := mem.RememberEvent(context.Background(), mnemos.Event{
			At: time.Now(), Type: "note", Content: fmt.Sprintf("Batch item %d written right before Close.", i),
		}); err != nil {
			t.Fatalf("RememberEvent %d: %v", i, err)
		}
	}
	if err := mem.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, eventVectors := embeddedCounts(t, dsn); eventVectors != events {
		t.Errorf("%d of %d event vectors after Close: queued embeds were discarded", eventVectors, events)
	}
}

// lockedBuffer is an io.Writer safe for the concurrent slog writes the embed
// goroutines make.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// When the provider has stalled, Close still gives up rather than hang. The
// loss must then be loud — counted, and logged once with the number abandoned
// and how to recover — and the abandoned items must stay reachable by
// `mnemos reembed`, which embeds the claims and events that have no vector.
func TestClose_AbandonedEmbedsAreCountedLoggedAndRecoverable(t *testing.T) {
	clearMnemosEnv(t)
	var logs lockedBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	dsn := "sqlite://" + filepath.Join(t.TempDir(), "brain.db")
	embedder := newBlockingEmbedder()
	mem, err := mnemos.New(mnemos.WithStorage(dsn), mnemos.WithSharedProvider(nil, embedder))
	if err != nil {
		t.Fatal(err)
	}
	const events = 10
	for i := range events {
		if err := mem.RememberEvent(context.Background(), mnemos.Event{
			At: time.Now(), Type: "note", Content: fmt.Sprintf("Stalled item %d.", i),
		}); err != nil {
			t.Fatalf("RememberEvent %d: %v", i, err)
		}
	}
	before := embedding.Failures()
	if err := mem.Close(); err != nil {
		t.Fatal(err)
	}
	if lost := embedding.Failures() - before; lost != events {
		t.Errorf("embed failure counter rose by %d, want %d (one per abandoned embed)", lost, events)
	}
	out := logs.String()
	if !strings.Contains(out, "abandoned=10") || !strings.Contains(out, "mnemos reembed") {
		t.Errorf("Close did not log the abandoned count and the recovery command; logs:\n%s", out)
	}

	ctx := context.Background()
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	stored, err := conn.Events.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := conn.Embeddings.ListByEntityType(ctx, "event")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != events || len(vectors) != 0 {
		t.Errorf("got %d events and %d vectors, want %d events with no vector for reembed to backfill", len(stored), len(vectors), events)
	}
}
