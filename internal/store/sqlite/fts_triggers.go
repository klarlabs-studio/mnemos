package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
)

// ftsTriggers keep events_fts and claims_fts in step with their base tables.
//
// The id columns are UNINDEXED in FTS5, so `WHERE claim_id = old.id` was a full
// scan of the index: ~85 ms per claim delete, and per upsert that rewrote the
// text, on a 66k-claim brain (#466). The *_fts_map tables record each base
// row's FTS rowid, so delete and update are point lookups. They key on the FTS
// rowid, not the base table's: claims and events have TEXT primary keys, so
// VACUUM may renumber their rowids, while an FTS5 rowid is stable.
//
// The update triggers fire only when the text actually changes. An upsert that
// rewrites a claim with its own text (every re-ingest, and the read-modify-write
// maintenance passes) leaves the index alone.
//
// Inside a trigger, last_insert_rowid() is the rowid of the trigger's own most
// recent INSERT, here the FTS row just written.
var ftsTriggers = []struct{ name, sql string }{
	{"events_ai_fts", `CREATE TRIGGER events_ai_fts AFTER INSERT ON events BEGIN
	INSERT INTO events_fts(event_id, content) VALUES (new.id, new.content);
	INSERT OR REPLACE INTO events_fts_map(event_id, fts_rowid) VALUES (new.id, last_insert_rowid());
END`},
	{"events_ad_fts", `CREATE TRIGGER events_ad_fts AFTER DELETE ON events BEGIN
	DELETE FROM events_fts WHERE rowid = (SELECT fts_rowid FROM events_fts_map WHERE event_id = old.id);
	DELETE FROM events_fts_map WHERE event_id = old.id;
END`},
	{"events_au_fts", `CREATE TRIGGER events_au_fts AFTER UPDATE OF content ON events WHEN old.content IS NOT new.content BEGIN
	UPDATE events_fts SET content = new.content WHERE rowid = (SELECT fts_rowid FROM events_fts_map WHERE event_id = old.id);
END`},
	{"claims_ai_fts", `CREATE TRIGGER claims_ai_fts AFTER INSERT ON claims BEGIN
	INSERT INTO claims_fts(claim_id, text) VALUES (new.id, new.text);
	INSERT OR REPLACE INTO claims_fts_map(claim_id, fts_rowid) VALUES (new.id, last_insert_rowid());
END`},
	{"claims_ad_fts", `CREATE TRIGGER claims_ad_fts AFTER DELETE ON claims BEGIN
	DELETE FROM claims_fts WHERE rowid = (SELECT fts_rowid FROM claims_fts_map WHERE claim_id = old.id);
	DELETE FROM claims_fts_map WHERE claim_id = old.id;
END`},
	{"claims_au_fts", `CREATE TRIGGER claims_au_fts AFTER UPDATE OF text ON claims WHEN old.text IS NOT new.text BEGIN
	UPDATE claims_fts SET text = new.text WHERE rowid = (SELECT fts_rowid FROM claims_fts_map WHERE claim_id = old.id);
END`},
}

// ensureFTSMaps installs ftsTriggers on a brain that lacks them or carries an
// older version, and rebuilds the maps they rely on. It runs on every open, not
// behind migrate's version gate: every brain from the previous release is
// already at currentSchemaVersion (see migrate). On an up-to-date brain it is
// one read of sqlite_master.
func ensureFTSMaps(db *sql.DB) error {
	current, err := ftsTriggersCurrent(db)
	if err != nil || current {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin fts trigger upgrade: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, trg := range ftsTriggers {
		if _, err := tx.Exec(`DROP TRIGGER IF EXISTS ` + trg.name); err != nil {
			return fmt.Errorf("drop trigger %s: %w", trg.name, err)
		}
	}
	if err := reconcileFTSMaps(tx); err != nil {
		return err
	}
	for _, trg := range ftsTriggers {
		if _, err := tx.Exec(trg.sql); err != nil {
			return fmt.Errorf("create trigger %s: %w", trg.name, err)
		}
	}
	return tx.Commit()
}

// ftsTriggersCurrent reports whether every trigger in ftsTriggers exists with
// exactly its current definition.
func ftsTriggersCurrent(db *sql.DB) (bool, error) {
	rows, err := db.Query(`SELECT name, sql FROM sqlite_master WHERE type = 'trigger' AND name LIKE '%\_fts' ESCAPE '\'`)
	if err != nil {
		return false, fmt.Errorf("read fts triggers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	installed := map[string]string{}
	for rows.Next() {
		var name, text string
		if err := rows.Scan(&name, &text); err != nil {
			return false, fmt.Errorf("read fts triggers: %w", err)
		}
		installed[name] = text
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("read fts triggers: %w", err)
	}
	for _, trg := range ftsTriggers {
		if strings.TrimSpace(installed[trg.name]) != trg.sql {
			return false, nil
		}
	}
	return true, nil
}

type sqlRunner interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// ftsIndex names one FTS index and the base table it mirrors.
type ftsIndex struct{ base, fts, idCol, textCol, mapTable string }

var ftsIndexes = []ftsIndex{
	{"events", "events_fts", "event_id", "content", "events_fts_map"},
	{"claims", "claims_fts", "claim_id", "text", "claims_fts_map"},
}

// reconcileFTSMaps rebuilds each rowid map from its FTS index, then repairs the
// index so it holds exactly one row per base row: no rows for deleted base rows,
// no duplicates (the old scanning delete removed every row with a matching id,
// a map points at one) and none missing.
//
// It runs on the open path, so the common case must stay cheap: one scan of
// each index to build the map, then checks that use only the map and the base
// table's primary key (about 2 s on a 2.6 GB, 66k-claim brain). Each repair
// rescans the index and runs only when its check finds something to fix.
func reconcileFTSMaps(db sqlRunner) error {
	for _, ix := range ftsIndexes {
		if err := reconcileFTSIndex(db, ix); err != nil {
			return fmt.Errorf("reconcile %s: %w", ix.fts, err)
		}
	}
	return nil
}

func reconcileFTSIndex(db sqlRunner, ix ftsIndex) error {
	exec := func(format string) error {
		_, err := db.Exec(fmt.Sprintf(format, ix.base, ix.fts, ix.idCol, ix.textCol, ix.mapTable))
		return err
	}
	count := func(format string) (int, error) {
		var n int
		err := db.QueryRow(fmt.Sprintf(format, ix.base, ix.fts, ix.idCol, ix.textCol, ix.mapTable)).Scan(&n)
		return n, err
	}
	// Placeholders: %[1]s base, %[2]s fts, %[3]s id column, %[4]s text column, %[5]s map.
	if err := exec(`DELETE FROM %[5]s`); err != nil {
		return err
	}
	if err := exec(`INSERT INTO %[5]s(%[3]s, fts_rowid) SELECT %[3]s, max(rowid) FROM %[2]s GROUP BY %[3]s`); err != nil {
		return err
	}

	indexed, err := count(`SELECT count(*) FROM %[2]s`)
	if err != nil {
		return err
	}
	mapped, err := count(`SELECT count(*) FROM %[5]s`)
	if err != nil {
		return err
	}
	if indexed != mapped {
		if err := exec(`DELETE FROM %[2]s WHERE rowid NOT IN (SELECT fts_rowid FROM %[5]s)`); err != nil {
			return err
		}
	}

	orphans, err := count(`SELECT count(*) FROM %[5]s WHERE %[3]s NOT IN (SELECT id FROM %[1]s)`)
	if err != nil {
		return err
	}
	if orphans > 0 {
		if err := exec(`DELETE FROM %[2]s WHERE rowid IN (SELECT fts_rowid FROM %[5]s WHERE %[3]s NOT IN (SELECT id FROM %[1]s))`); err != nil {
			return err
		}
		if err := exec(`DELETE FROM %[5]s WHERE %[3]s NOT IN (SELECT id FROM %[1]s)`); err != nil {
			return err
		}
	}

	missing, err := count(`SELECT count(*) FROM %[1]s WHERE id NOT IN (SELECT %[3]s FROM %[5]s)`)
	if err != nil {
		return err
	}
	if missing > 0 {
		if err := exec(`INSERT INTO %[2]s(%[3]s, %[4]s) SELECT id, %[4]s FROM %[1]s WHERE id NOT IN (SELECT %[3]s FROM %[5]s)`); err != nil {
			return err
		}
		if err := exec(`INSERT INTO %[5]s(%[3]s, fts_rowid) SELECT %[3]s, rowid FROM %[2]s WHERE %[3]s NOT IN (SELECT %[3]s FROM %[5]s)`); err != nil {
			return err
		}
	}
	return nil
}
