package pipeline

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// ADR 0038: the shared ingest write records the dependencies an episode
// declares, so capture, process, file ingestion and the benchmark seed all
// detect them the same way. Asserted on what the store returns.
func TestPersistArtifacts_RecordsDeclaredDependencies(t *testing.T) {
	ctx := context.Background()
	for _, dsn := range []string{"memory://deps", "sqlite://" + filepath.Join(t.TempDir(), "deps.db")} {
		conn, err := store.Open(ctx, dsn)
		if err != nil {
			t.Fatalf("open %s: %v", dsn, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		now := time.Now().UTC()
		event := func(id, source, content string) domain.Event {
			return domain.Event{ID: id, RunID: "r", SchemaVersion: "1.0", Content: content, SourceInputID: source, Timestamp: now, IngestedAt: now, CreatedBy: "author-" + id}
		}
		if err := PersistArtifacts(ctx, conn, []domain.Event{event("adv", "vendor-advisory", "libfoo 2.3 fixed the overflow.")}, nil, nil, nil); err != nil {
			t.Fatalf("%s: persist advisory: %v", dsn, err)
		}
		if err := PersistArtifacts(ctx, conn, []domain.Event{
			event("blog", "security-blog", "libfoo 2.3 fixed the overflow.\nSource: the vendor advisory."),
			event("notes", "release-notes", "libfoo 2.3 was released in March."),
		}, nil, nil, nil); err != nil {
			t.Fatalf("%s: persist blog and notes: %v", dsn, err)
		}
		deps, err := conn.Dependencies.ListDependents(ctx, []string{"adv"})
		if err != nil {
			t.Fatalf("%s: dependents: %v", dsn, err)
		}
		if len(deps) != 1 || deps[0].EventID != "blog" || deps[0].Kind != domain.DependencyCites ||
			deps[0].Signal != domain.SignalNamedSource || deps[0].CreatedBy != "author-blog" {
			t.Errorf("%s: dependents of the advisory = %+v, want the blog alone, attributed to its author", dsn, deps)
		}
		if out, err := conn.Dependencies.ListByEventIDs(ctx, []string{"notes"}); err != nil || len(out) != 0 {
			t.Errorf("%s: the uncited release notes depend on %+v (%v), want nothing", dsn, out, err)
		}
	}
}
