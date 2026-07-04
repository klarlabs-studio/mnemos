package postgres_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// TestPostgres_FullTextSearch exercises the sparse (full-text) recall leg added
// for hybrid retrieval (R1): the generated search_tsv column + GIN index and the
// websearch_to_tsquery match. Gated on TEST_POSTGRES_DSN like the rest of this
// file. It proves exact-token recall — the very thing dense cosine underweights.
func TestPostgres_FullTextSearch(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// The repositories must advertise the TextSearcher capability once the
	// schema's generated tsvector column exists — this is what new.go type-
	// asserts to wire the hybrid path.
	searcher, ok := conn.Events.(ports.TextSearcher)
	if !ok {
		t.Fatal("EventRepository must implement ports.TextSearcher for hybrid retrieval")
	}

	appendEv := func(id, content string) {
		if err := conn.Events.Append(ctx, domain.Event{
			ID: id, RunID: "run-A", SchemaVersion: "1", Content: content,
			SourceInputID: "in", Timestamp: now, IngestedAt: now,
			Metadata: map[string]string{}, CreatedBy: domain.SystemUser,
		}); err != nil {
			t.Fatalf("Append %s: %v", id, err)
		}
	}
	appendEv("ev-sha", "deploy commit abc123def reverted after the payments incident")
	appendEv("ev-other", "the analytics dashboard shipped on schedule")

	// Exact-token query: only the event containing the SHA should match.
	hits, err := searcher.SearchByText(ctx, "abc123def", 10)
	if err != nil {
		t.Fatalf("SearchByText: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "ev-sha" {
		t.Fatalf("exact-token search = %+v, want single hit ev-sha", hits)
	}

	// Multi-term query ranks the on-topic event; the unrelated one may or may
	// not match, but the incident event must rank first when it does.
	hits, err = searcher.SearchByText(ctx, "payments incident", 10)
	if err != nil {
		t.Fatalf("SearchByText multi-term: %v", err)
	}
	if len(hits) == 0 || hits[0].ID != "ev-sha" {
		t.Fatalf("multi-term search = %+v, want ev-sha ranked first", hits)
	}

	// websearch_to_tsquery must tolerate junk / operator input without erroring
	// (this path runs on every query), and an all-stopword/empty query returns
	// no rows rather than a parse error.
	for _, q := range []string{`"unterminated`, "   ", "and the of"} {
		if _, err := searcher.SearchByText(ctx, q, 10); err != nil {
			t.Fatalf("SearchByText(%q) must not error: %v", q, err)
		}
	}

	// Claims repository exposes the same capability over its own text column.
	if _, ok := conn.Claims.(ports.TextSearcher); !ok {
		t.Fatal("ClaimRepository must implement ports.TextSearcher")
	}
}
