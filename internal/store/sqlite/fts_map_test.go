package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// The FTS triggers used to find a row with `WHERE claim_id = old.id`, a filter
// on an UNINDEXED column, so every claim delete and every upsert that rewrote
// text scanned all of claims_fts: ~85 ms each on a 66k-claim brain (#466). A
// semantic-dedupe merge paid two scans, and a consolidate that held the write
// lock through thousands of them starved every other writer for hours.

func openFTSTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := open(filepath.Join(t.TempDir(), "fts_map.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func insertFTSClaim(t *testing.T, db *sql.DB, id, text string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO claims (id, text, type, confidence, status, created_at) VALUES (?, ?, 'fact', 0.8, 'active', ?)`,
		id, text, nowRFC(),
	); err != nil {
		t.Fatalf("insert claim %s: %v", id, err)
	}
}

func insertFTSEvent(t *testing.T, db *sql.DB, id, content string) {
	t.Helper()
	now := nowRFC()
	if _, err := db.Exec(
		`INSERT INTO events (id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, ingested_at)
		 VALUES (?, 'r', 'v1', ?, 'src', ?, '{}', ?)`,
		id, content, now, now,
	); err != nil {
		t.Fatalf("insert event %s: %v", id, err)
	}
}

func ftsClaimHits(t *testing.T, db *sql.DB, term string) []string {
	t.Helper()
	return ftsIDs(t, db, `SELECT claim_id FROM claims_fts WHERE claims_fts MATCH ? ORDER BY claim_id`, term)
}

func ftsEventHits(t *testing.T, db *sql.DB, term string) []string {
	t.Helper()
	return ftsIDs(t, db, `SELECT event_id FROM events_fts WHERE events_fts MATCH ? ORDER BY event_id`, term)
}

func ftsIDs(t *testing.T, db *sql.DB, q, term string) []string {
	t.Helper()
	rows, err := db.Query(q, term)
	if err != nil {
		t.Fatalf("fts query %q: %v", term, err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// assertFTSInStep checks the index holds exactly one row per base row, and the
// map points every base row at its own index row.
func assertFTSInStep(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`SELECT count(*) FROM claims c WHERE NOT EXISTS (SELECT 1 FROM claims_fts_map m JOIN claims_fts f ON f.rowid = m.fts_rowid WHERE m.claim_id = c.id AND f.claim_id = c.id)`,
		`SELECT (SELECT count(*) FROM claims_fts) - (SELECT count(*) FROM claims)`,
		`SELECT (SELECT count(*) FROM claims_fts_map) - (SELECT count(*) FROM claims)`,
		`SELECT count(*) FROM events e WHERE NOT EXISTS (SELECT 1 FROM events_fts_map m JOIN events_fts f ON f.rowid = m.fts_rowid WHERE m.event_id = e.id AND f.event_id = e.id)`,
		`SELECT (SELECT count(*) FROM events_fts) - (SELECT count(*) FROM events)`,
		`SELECT (SELECT count(*) FROM events_fts_map) - (SELECT count(*) FROM events)`,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if n != 0 {
			t.Fatalf("FTS out of step (%d): %s", n, q)
		}
	}
}

func TestFTSTriggers_KeepIndexInStep(t *testing.T) {
	db := openFTSTestDB(t)
	insertFTSClaim(t, db, "c1", "the cache warms on boot")
	insertFTSClaim(t, db, "c2", "the cache is cold after deploy")
	insertFTSEvent(t, db, "e1", "deploy finished at noon")
	insertFTSEvent(t, db, "e2", "deploy rolled back")
	assertFTSInStep(t, db)
	if current, err := ftsTriggersCurrent(db); err != nil || !current {
		t.Fatalf("a freshly opened brain must not need a trigger upgrade on its next open (%v, %v)", current, err)
	}

	if _, err := db.Exec(`UPDATE claims SET text = 'the queue drains on boot' WHERE id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	if got := ftsClaimHits(t, db, "cache"); len(got) != 1 || got[0] != "c2" {
		t.Fatalf("old text still indexed after update: %v", got)
	}
	if got := ftsClaimHits(t, db, "queue"); len(got) != 1 || got[0] != "c1" {
		t.Fatalf("new text not indexed after update: %v", got)
	}

	if _, err := db.Exec(`DELETE FROM claims WHERE id = 'c2'`); err != nil {
		t.Fatal(err)
	}
	if got := ftsClaimHits(t, db, "cache"); len(got) != 0 {
		t.Fatalf("deleted claim still indexed: %v", got)
	}
	if _, err := db.Exec(`DELETE FROM events WHERE id = 'e2'`); err != nil {
		t.Fatal(err)
	}
	if got := ftsEventHits(t, db, "deploy"); len(got) != 1 || got[0] != "e1" {
		t.Fatalf("deleted event still indexed: %v", got)
	}
	assertFTSInStep(t, db)
}

// An upsert that rewrites a claim with its own text (every re-ingest, and the
// read-modify-write passes) must not touch the index at all.
func TestFTSTriggers_UnchangedTextUpsertSkipsIndex(t *testing.T) {
	db := openFTSTestDB(t)
	insertFTSClaim(t, db, "c1", "the cache warms on boot")

	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	changes := func() int {
		var n int
		if err := conn.QueryRowContext(t.Context(), `SELECT total_changes()`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := changes()
	if _, err := conn.ExecContext(t.Context(),
		`INSERT INTO claims (id, text, type, confidence, status, created_at) VALUES ('c1', 'the cache warms on boot', 'fact', 0.9, 'active', ?)
		 ON CONFLICT(id) DO UPDATE SET text = excluded.text, confidence = excluded.confidence`, nowRFC()); err != nil {
		t.Fatal(err)
	}
	if d := changes() - before; d != 1 {
		t.Fatalf("unchanged-text upsert made %d row changes, want 1 (the claim only)", d)
	}
	if got := ftsClaimHits(t, db, "cache"); len(got) != 1 {
		t.Fatalf("claim lost from index: %v", got)
	}
}

// The scan-free lookup must not depend on base-table rowids: claims and events
// have TEXT primary keys, so VACUUM may renumber their rowids.
func TestFTSTriggers_SurviveVacuum(t *testing.T) {
	db := openFTSTestDB(t)
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		insertFTSClaim(t, db, id, "belief "+id+" about vacuum")
	}
	if _, err := db.Exec(`DELETE FROM claims WHERE id IN ('c1', 'c2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM claims WHERE id = 'c3'`); err != nil {
		t.Fatal(err)
	}
	if got := ftsClaimHits(t, db, "vacuum"); len(got) != 1 || got[0] != "c4" {
		t.Fatalf("index wrong after VACUUM: %v", got)
	}
	assertFTSInStep(t, db)
}

// The pre-#466 triggers, as every existing brain has them.
const legacyFTSTriggers = `
DROP TRIGGER IF EXISTS events_ai_fts;
DROP TRIGGER IF EXISTS events_ad_fts;
DROP TRIGGER IF EXISTS events_au_fts;
DROP TRIGGER IF EXISTS claims_ai_fts;
DROP TRIGGER IF EXISTS claims_ad_fts;
DROP TRIGGER IF EXISTS claims_au_fts;
DROP TABLE IF EXISTS events_fts_map;
DROP TABLE IF EXISTS claims_fts_map;
CREATE TRIGGER events_ai_fts AFTER INSERT ON events BEGIN
	INSERT INTO events_fts(event_id, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER events_ad_fts AFTER DELETE ON events BEGIN
	DELETE FROM events_fts WHERE event_id = old.id;
END;
CREATE TRIGGER events_au_fts AFTER UPDATE OF content ON events BEGIN
	UPDATE events_fts SET content = new.content WHERE event_id = old.id;
END;
CREATE TRIGGER claims_ai_fts AFTER INSERT ON claims BEGIN
	INSERT INTO claims_fts(claim_id, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER claims_ad_fts AFTER DELETE ON claims BEGIN
	DELETE FROM claims_fts WHERE claim_id = old.id;
END;
CREATE TRIGGER claims_au_fts AFTER UPDATE OF text ON claims BEGIN
	UPDATE claims_fts SET text = new.text WHERE claim_id = old.id;
END;
`

// A brain from the previous release is already at currentSchemaVersion and
// carries the scanning triggers and no map. Reopening it must convert it.
func TestBootstrap_UpgradesLegacyFTSTriggers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy_fts.db")
	db, err := open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(legacyFTSTriggers); err != nil {
		t.Fatalf("install legacy triggers: %v", err)
	}
	insertFTSClaim(t, db, "c1", "legacy belief about kafka")
	insertFTSClaim(t, db, "c2", "legacy belief about redis")
	insertFTSEvent(t, db, "e1", "legacy event about kafka")
	// A stray duplicate index row, which the scanning delete would also have
	// removed; the conversion must not leave it behind as an orphan.
	if _, err := db.Exec(`INSERT INTO claims_fts(claim_id, text) VALUES ('c1', 'legacy belief about kafka')`); err != nil {
		t.Fatal(err)
	}
	// An index row whose claim is gone, and a claim with no index row.
	if _, err := db.Exec(`INSERT INTO claims_fts(claim_id, text) VALUES ('gone', 'orphaned belief about kafka')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM events_fts WHERE event_id = 'e1'`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	db, err = open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = db.Close() }()

	for _, trg := range []string{"claims_ad_fts", "claims_au_fts", "events_ad_fts", "events_au_fts"} {
		var sqlText string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trg).Scan(&sqlText); err != nil {
			t.Fatalf("trigger %s: %v", trg, err)
		}
		if !strings.Contains(sqlText, "_fts_map") {
			t.Fatalf("trigger %s not upgraded:\n%s", trg, sqlText)
		}
	}
	assertFTSInStep(t, db)
	if got := ftsEventHits(t, db, "kafka"); len(got) != 1 || got[0] != "e1" {
		t.Fatalf("event missing from the index was not restored: %v", got)
	}

	if _, err := db.Exec(`DELETE FROM claims WHERE id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	if got := ftsClaimHits(t, db, "kafka"); len(got) != 0 {
		t.Fatalf("deleted legacy claim still indexed: %v", got)
	}
	insertFTSClaim(t, db, "c3", "fresh belief about kafka")
	if got := ftsClaimHits(t, db, "kafka"); len(got) != 1 || got[0] != "c3" {
		t.Fatalf("new claim not indexed after upgrade: %v", got)
	}
	assertFTSInStep(t, db)
}
