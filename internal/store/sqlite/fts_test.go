package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSanitizeFTSQuery(t *testing.T) {
	cases := map[string]string{
		"simple words":          "simple words",
		"":                      "",
		"  spaced  ":            "spaced",
		`"quoted phrase"`:       "quoted phrase",
		"foo:bar":               "foo bar",
		"a OR b AND c NOT d":    "a b c d",
		"or AND not":            "",
		"hello -world":          "hello world",
		"PostgreSQL upgrade":    "PostgreSQL upgrade",
		"what's the matter?":    "what s the matter",
		"(grouped) [bracketed]": "grouped bracketed",
	}
	for in, want := range cases {
		if got := sanitizeFTSQuery(in); got != want {
			t.Errorf("sanitizeFTSQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSearchByText_ReturnsRelevantEvents(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "fts.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	now := nowRFC()
	for _, p := range []struct{ id, content string }{
		{"ev1", "Felix decided to migrate to PostgreSQL"},
		{"ev2", "The team agreed on Slack and Notion as the daily tools"},
		{"ev3", "Acme launched the new product line in Berlin"},
	} {
		if _, err := db.Exec(
			`INSERT INTO events (id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, ingested_at)
			 VALUES (?, 'r', 'v1', ?, 'src', ?, '{}', ?)`,
			p.id, p.content, now, now,
		); err != nil {
			t.Fatalf("seed event %s: %v", p.id, err)
		}
	}

	hits, err := NewEventRepository(db).SearchByText(ctx, "PostgreSQL", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "ev1" {
		t.Fatalf("expected only ev1 for PostgreSQL, got %+v", hits)
	}
	if hits[0].Score <= 0 {
		t.Fatalf("expected positive score (sign-flipped bm25), got %v", hits[0].Score)
	}
}

// The 'porter unicode61' tokenizer stems inflected forms, so a query recalls
// documents phrased with a different inflection of the same word. Before the
// fix, the default unicode61 tokenizer matched token-for-token and these
// missed entirely.
func TestSearchByText_StemsInflectedForms(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "stem.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	now := nowRFC()
	if _, err := db.Exec(
		`INSERT INTO events (id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, ingested_at)
		 VALUES ('ev1', 'r', 'v1', 'The system issues automatic refunds to customers', 'src', ?, '{}', ?)`,
		now, now,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Each query word is a different inflection of a word in the stored
	// text: "refund" vs "refunds", "issued" vs "issues", "customer" vs
	// "customers". All recall ev1 only because the tokenizer stems.
	for _, q := range []string{"refund", "issued", "customer"} {
		hits, err := NewEventRepository(db).SearchByText(ctx, q, 5)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if len(hits) != 1 || hits[0].ID != "ev1" {
			t.Fatalf("query %q: expected ev1 via stemming, got %+v", q, hits)
		}
	}
}

func TestSearchByText_ToleratesMessyInput(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "fts.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	now := nowRFC()
	if _, err := db.Exec(
		`INSERT INTO events (id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, ingested_at)
		 VALUES ('ev1', 'r', 'v1', 'Felix decided on Postgres', 'src', ?, '{}', ?)`,
		now, now,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Quotes / colons / parens would all blow up an FTS5 parse if
	// passed through verbatim. SearchByText should just return the
	// natural-language match (only words present in the seed text
	// stay in the query after sanitisation; logical operators and
	// punctuation drop).
	hits, err := NewEventRepository(db).SearchByText(ctx, `decided "Postgres" :: ?`, 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
}

func TestSearchByText_BackfillsLegacyEventsViaMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	now := nowRFC()

	// Seed a v4-shaped DB: events table without an FTS counterpart.
	{
		raw, err := open(path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		// Knock the FTS index back to empty and force user_version to 4
		// to simulate a v0.9 → v0.10 upgrade.
		if _, err := raw.Exec(`
			DELETE FROM events_fts;
			DELETE FROM claims_fts;
			PRAGMA user_version = 4;
		`); err != nil {
			t.Fatalf("seed v4 state: %v", err)
		}
		if _, err := raw.Exec(
			`INSERT INTO events (id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, ingested_at)
			 VALUES ('ev_legacy', 'r', 'v1', 'A long-forgotten event about coffee', 'src', ?, '{}', ?)`,
			now, now,
		); err != nil {
			t.Fatalf("seed legacy event: %v", err)
		}
		_ = raw.Close()
	}

	// Re-open: migrate should backfill events_fts.
	db, err := open(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	hits, err := NewEventRepository(db).SearchByText(context.Background(), "coffee", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "ev_legacy" {
		t.Fatalf("expected backfilled ev_legacy, got %+v", hits)
	}
}
