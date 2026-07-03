package mnemos_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/providers"

	_ "go.klarlabs.de/mnemos/internal/store/postgres"
)

// fakeSingleEmbedder is a deterministic single-text embedder for tests.
type fakeSingleEmbedder struct{}

func (fakeSingleEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	v := make([]float32, 16)
	for i, r := range text {
		v[i%16] += float32((r % 11) + 1)
	}
	return v, nil
}

// TestEmbedOnWrite proves the async embed-on-write path: after a write, an entry
// appears in the embeddings table (so recall can rank by vector, not just token
// overlap). Gated on TEST_POSTGRES_DSN.
func TestEmbedOnWrite(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping embed-on-write test")
	}
	clearMnemosEnv(t)
	ns := fmt.Sprintf("eow_%d", time.Now().UnixNano())
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	mem, err := mnemos.New(
		mnemos.WithStorage(dsn+sep+"namespace="+ns),
		mnemos.WithSharedProvider(nil, providers.EmbedderFromSingle(fakeSingleEmbedder{})),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = mem.Close() }()
	if got := mem.Info().Embedder; got != "shared" {
		t.Fatalf("expected shared embedder mode, got %q", got)
	}

	ctx := context.Background()
	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "e1", At: time.Now().UTC(), Type: "observation", Content: "the payments latency spike came from a slow query"}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}

	// The embed is async; poll the embeddings table until the event vector lands.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	t.Cleanup(func() { _, _ = db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, ns)) })

	var n int
	for i := 0; i < 60; i++ {
		if err := db.QueryRow(fmt.Sprintf(`SELECT count(*) FROM %s.embeddings WHERE entity_type='event'`, ns)).Scan(&n); err == nil && n > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n == 0 {
		t.Fatal("no event embedding stored — embed-on-write did not populate the embeddings table")
	}
}
