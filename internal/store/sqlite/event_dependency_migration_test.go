package sqlite

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// A brain written by the v37 binary has event_dependencies without the
// tombstone columns. Opening it with this binary adds them, keeps every
// existing row live, and the new operations work on it.
func TestMigrate_AddsDependencyTombstoneColumnsToAV37Brain(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	ctx := context.Background()

	for _, stmt := range []string{
		`DROP TABLE event_dependencies`,
		`CREATE TABLE event_dependencies (
			event_id TEXT NOT NULL, depends_on TEXT NOT NULL, kind TEXT NOT NULL, signal_type TEXT NOT NULL,
			reference_text TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, created_by TEXT NOT NULL DEFAULT '<system>',
			PRIMARY KEY (event_id, depends_on))`,
		`INSERT INTO event_dependencies VALUES ('ev-copy', 'ev-src', 'cites', 'link', 'https://src', '2026-11-01T00:00:00Z', 'x')`,
		`PRAGMA user_version = 37`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewEventDependencyRepository(db)
	live, err := repo.ListByEventIDs(ctx, []string{"ev-copy"})
	if err != nil || len(live) != 1 || !live[0].Live() {
		t.Fatalf("pre-existing row after migration = %+v (%v), want it live", live, err)
	}
	if _, err := repo.Remove(ctx, "ev-copy", "ev-src", "reviewer", "", time.Now()); err != nil {
		t.Fatalf("remove on a migrated brain: %v", err)
	}
	if _, err := repo.Declare(ctx, domain.EventDependency{EventID: "ev-copy", DependsOn: "ev-src", Kind: domain.DependencyCopies,
		Signal: domain.SignalDeclared, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("revive on a migrated brain: %v", err)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != currentSchemaVersion {
		t.Errorf("user_version = %d (%v), want %d", version, err, currentSchemaVersion)
	}
}

// A brain written by the v38 binary has tombstones but no removal_reason and
// no event_dependency_history. Opening it adds both; an existing tombstone is
// already one closed interval (no backfill), reads an empty reason, and moves
// into the history, unchanged, on its first revival.
func TestMigrate_AV38TombstoneBecomesAClosedInterval(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	ctx := context.Background()

	for _, stmt := range []string{
		`DROP TABLE event_dependencies`,
		`DROP TABLE event_dependency_history`,
		`CREATE TABLE event_dependencies (
			event_id TEXT NOT NULL, depends_on TEXT NOT NULL, kind TEXT NOT NULL, signal_type TEXT NOT NULL,
			reference_text TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, created_by TEXT NOT NULL DEFAULT '<system>',
			removed_at TEXT, removed_by TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (event_id, depends_on))`,
		`INSERT INTO event_dependencies VALUES ('ev-copy', 'ev-src', 'cites', 'link', 'https://src', '2026-11-01T00:00:00Z', 'x', '2026-11-02T00:00:00Z', 'reviewer')`,
		`PRAGMA user_version = 38`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := ensureSchema(db); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}
	repo := NewEventDependencyRepository(db)
	all, err := repo.ListByEventIDsWithRemoved(ctx, []string{"ev-copy"})
	if err != nil || len(all) != 1 || all[0].RemovedAt == nil || all[0].RemovedBy != "reviewer" || all[0].RemovalReason != "" {
		t.Fatalf("v38 tombstone after migration = %+v (%v), want one closed interval with no reason", all, err)
	}
	if _, err := repo.Declare(ctx, domain.EventDependency{EventID: "ev-copy", DependsOn: "ev-src", Kind: domain.DependencyCopies,
		Signal: domain.SignalDeclared, CreatedAt: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("revive on a migrated brain: %v", err)
	}
	all, err = repo.ListByEventIDsWithRemoved(ctx, []string{"ev-copy"})
	if err != nil || len(all) != 2 {
		t.Fatalf("intervals after revival = %+v (%v), want the v38 one kept", all, err)
	}
	inFirst := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if kept := all[0]; kept.Signal != domain.SignalLink || kept.RemovedBy != "reviewer" || !kept.LiveAt(inFirst) {
		t.Errorf("kept interval = %+v, want the detected link, removed by reviewer, in force on 1 November", kept)
	}
}
