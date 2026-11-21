package store_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/pipeline"
)

// Every backend must persist which extractor produced a claim (#445 item 3,
// ADR 0029 §3), asserted on what the STORE returns after the real ingest path,
// never on what the extractor handed the write call: a column missing from a
// hand-written backend's INSERT or projection still commits and still scans,
// and reads back a zero indistinguishable from "never set" (#331, #335).
func TestPersistArtifacts_PersistsExtractedBy(t *testing.T) {
	backends := openBackends(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

	for _, b := range backends {
		event := domain.Event{
			ID: "ev-extracted-by", RunID: "run-extracted-by", SchemaVersion: "1",
			Content:       "The billing service retries failed webhooks three times.",
			SourceInputID: "in-1", Timestamp: at, IngestedAt: at, CreatedBy: domain.SystemUser,
		}
		claims, links, err := extract.NewEngine().Extract(ctx, []domain.Event{event})
		if err != nil {
			t.Fatalf("%s: extract: %v", b.name, err)
		}
		if len(claims) != 1 {
			t.Fatalf("%s: extracted %d claims, want 1", b.name, len(claims))
		}
		if err := pipeline.PersistArtifacts(ctx, b.conn, []domain.Event{event}, claims, links, nil); err != nil {
			t.Fatalf("%s: persist artifacts: %v", b.name, err)
		}

		stored, err := b.conn.Claims.ListByIDs(ctx, []string{claims[0].ID})
		if err != nil || len(stored) != 1 {
			t.Fatalf("%s: read back: %v (%d rows)", b.name, err, len(stored))
		}
		if stored[0].ExtractedBy != extract.RuleExtractorVersion {
			t.Errorf("%s: extracted_by read back %q, want %q — the producer was dropped at the store boundary",
				b.name, stored[0].ExtractedBy, extract.RuleExtractorVersion)
		}

		// A later write that does not know the producer (a read-modify-write
		// maintenance pass, an API writer that hits an existing id) must not
		// erase it. An explicit value still wins.
		rewrite := stored[0]
		rewrite.ExtractedBy = ""
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{rewrite}); err != nil {
			t.Fatalf("%s: rewrite: %v", b.name, err)
		}
		again, err := b.conn.Claims.ListByIDs(ctx, []string{claims[0].ID})
		if err != nil || len(again) != 1 {
			t.Fatalf("%s: read back after rewrite: %v", b.name, err)
		}
		if again[0].ExtractedBy != extract.RuleExtractorVersion {
			t.Errorf("%s: a write without a producer erased extracted_by: got %q", b.name, again[0].ExtractedBy)
		}

		rewrite.ExtractedBy = "llm/test:model/v1.6"
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{rewrite}); err != nil {
			t.Fatalf("%s: re-extract: %v", b.name, err)
		}
		final, err := b.conn.Claims.ListByIDs(ctx, []string{claims[0].ID})
		if err != nil || len(final) != 1 {
			t.Fatalf("%s: read back after re-extract: %v", b.name, err)
		}
		if final[0].ExtractedBy != "llm/test:model/v1.6" {
			t.Errorf("%s: an explicit producer did not win: got %q", b.name, final[0].ExtractedBy)
		}
	}
}
