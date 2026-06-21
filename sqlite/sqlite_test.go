package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
	_ "go.klarlabs.de/mnemos/sqlite"
)

// TestSQLiteProviderRegistered verifies that blank-importing the public sqlite
// package is sufficient for an external caller to open a SQLite-backed store —
// the registration the internal package performs is reachable through this
// re-export.
func TestSQLiteProviderRegistered(t *testing.T) {
	mem, err := mnemos.New(mnemos.WithSQLite(filepath.Join(t.TempDir(), "m.db")))
	if err != nil {
		t.Fatalf("New with sqlite provider: %v", err)
	}
	defer func() { _ = mem.Close() }()

	ctx := context.Background()
	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "e1", At: time.Now(), Type: "deployment", Content: "deployed v1"}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}
	id, err := mem.RememberClaim(ctx, mnemos.ClaimItem{Text: "service runs v1", EventIDs: []string{"e1"}})
	if err != nil {
		t.Fatalf("RememberClaim: %v", err)
	}
	if id == "" {
		t.Fatal("empty claim id")
	}
}
