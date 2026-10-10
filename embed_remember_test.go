package mnemos_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/providers"
	_ "go.klarlabs.de/mnemos/sqlite"
)

// slowEmbedder answers after a fixed delay, like a real provider, and honours
// cancellation, so an embed cancelled by Close stores nothing.
type slowEmbedder struct{ delay time.Duration }

func (e slowEmbedder) Embed(ctx context.Context, in providers.EmbedInput) (providers.EmbedOutput, error) {
	select {
	case <-time.After(e.delay):
	case <-ctx.Done():
		return providers.EmbedOutput{}, ctx.Err()
	}
	vectors := make([][]float32, len(in.Texts))
	for i := range vectors {
		vectors[i] = []float32{0.1, 0.2, 0.3}
	}
	return providers.EmbedOutput{Vectors: vectors, Model: "test-embed"}, nil
}

// embeddedCounts reopens the brain at dsn and counts stored claims and the
// claim and event vectors, after the memory that wrote them has closed.
func embeddedCounts(t *testing.T, dsn string) (claims, claimVectors, eventVectors int) {
	t.Helper()
	ctx := context.Background()
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	n, err := conn.Claims.CountAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cv, err := conn.Embeddings.ListByEntityType(ctx, "claim")
	if err != nil {
		t.Fatal(err)
	}
	ev, err := conn.Embeddings.ListByEntityType(ctx, "event")
	if err != nil {
		t.Fatal(err)
	}
	return int(n), len(cv), len(ev)
}

// Remember is the write path that extracts beliefs. With an embedder
// configured it must store a vector for each belief and episode it writes, as
// RememberClaim and RememberEvent do; otherwise recall finds those beliefs by
// token overlap only (#449).
func TestRemember_EmbedsWhatItWrites(t *testing.T) {
	clearMnemosEnv(t)
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "brain.db")
	mem, err := mnemos.New(mnemos.WithStorage(dsn), mnemos.WithSharedProvider(nil, slowEmbedder{delay: 10 * time.Millisecond}))
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Remember(context.Background(), mnemos.Item{
		Content: "The user adopted a beagle named Biscuit. The user moved to Berlin for a job at a bakery.",
	}); err != nil {
		t.Fatal(err)
	}
	if err := mem.Close(); err != nil {
		t.Fatal(err)
	}
	claims, claimVectors, eventVectors := embeddedCounts(t, dsn)
	if claims == 0 {
		t.Fatal("Remember extracted no claims; the test cannot check their vectors")
	}
	if claimVectors != claims {
		t.Errorf("%d of %d claims embedded, want all", claimVectors, claims)
	}
	if eventVectors == 0 {
		t.Error("the episode Remember stored was not embedded")
	}
}

// A process that writes and then closes at once (a CLI call, a capture hook,
// an MCP handler's deferred Close) must keep the vectors for what it wrote.
// Close used to cancel in-flight embeds before waiting for them, so they were
// discarded (#449).
func TestClose_LetsInFlightEmbedsFinish(t *testing.T) {
	clearMnemosEnv(t)
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "brain.db")
	mem, err := mnemos.New(mnemos.WithStorage(dsn), mnemos.WithSharedProvider(nil, slowEmbedder{delay: 300 * time.Millisecond}))
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.RememberEvent(context.Background(), mnemos.Event{
		At: time.Now(), Type: "note", Content: "Written immediately before Close.",
	}); err != nil {
		t.Fatal(err)
	}
	if err := mem.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, eventVectors := embeddedCounts(t, dsn); eventVectors != 1 {
		t.Errorf("%d event vectors after Close, want 1: the in-flight embed was discarded", eventVectors)
	}
}

// Back-to-back Remember calls with embedding on. Each Remember leaves vector
// writes in flight, and the next one's claim upsert used to collide with them
// and fail with "database is locked" (SQLITE_BUSY_SNAPSHOT). Found when 9 of
// 10 LoCoMo conversations failed to ingest in the #441 harness.
func TestRemember_ConsecutiveWritesWithEmbeddingSucceed(t *testing.T) {
	clearMnemosEnv(t)
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "brain.db")
	mem, err := mnemos.New(mnemos.WithStorage(dsn), mnemos.WithSharedProvider(nil, slowEmbedder{delay: 20 * time.Millisecond}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mem.Close() }()
	for i := range 25 {
		err := mem.Remember(context.Background(), mnemos.Item{
			Content: fmt.Sprintf("Session %d: the user moved the release to Friday %d. The user prefers tea over coffee in session %d.", i, i, i),
		})
		if err != nil {
			t.Fatalf("Remember %d: %v", i, err)
		}
	}
}
