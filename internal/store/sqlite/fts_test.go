package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestFTSMatchQuery(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"   ":                    "",
		"PostgreSQL upgrade":     `"postgresql" OR "upgrade"`,
		`"quoted" foo:bar (x)`:   `"quoted" OR "foo" OR "bar"`,
		"what's the 2.5 matter?": `"2" OR "5" OR "matter"`,
		"or AND not":             `"or" OR "and" OR "not"`,
	}
	for in, want := range cases {
		if got := ftsMatchQuery(in); got != want {
			t.Errorf("ftsMatchQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

// A natural-language question must reach the record that answers it. FTS5
// ANDs bare terms, so the old query required "when" and "did" to appear in
// the stored text and this returned nothing.
func TestSearchByText_QuestionFindsAnsweringRecord(t *testing.T) {
	db := seedEvents(t, map[string]string{
		"ev1": "Caroline went to the LGBTQ support group on 7 May 2023",
		"ev2": "Melanie painted a sunrise over the lake",
	})
	hits, err := NewEventRepository(db).SearchByText(context.Background(),
		"When did Caroline go to the LGBTQ support group?", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 || hits[0].ID != "ev1" {
		t.Fatalf("expected ev1 ranked first, got %+v", hits)
	}
}

// Punctuation inside a token ("2.5", "mg,") used to reach FTS5 verbatim and
// raise a syntax error, which recall swallowed as "no lexical hits".
func TestSearchByText_PunctuationInsideTermsDoesNotError(t *testing.T) {
	db := seedEvents(t, map[string]string{"ev1": "The vet prescribed 2.5 mg of meloxicam"})
	hits, err := NewEventRepository(db).SearchByText(context.Background(), "how much meloxicam, 2.5 mg?", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "ev1" {
		t.Fatalf("expected ev1, got %+v", hits)
	}
}

func seedEvents(t *testing.T, contents map[string]string) *sql.DB {
	t.Helper()
	db, err := open(filepath.Join(t.TempDir(), "fts.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := nowRFC()
	for id, content := range contents {
		if _, err := db.Exec(
			`INSERT INTO events (id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, ingested_at)
			 VALUES (?, 'r', 'v1', ?, 'src', ?, '{}', ?)`,
			id, content, now, now,
		); err != nil {
			t.Fatalf("seed event %s: %v", id, err)
		}
	}
	return db
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
