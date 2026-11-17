package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/ingest"
	"go.klarlabs.de/mnemos/internal/parser"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/relate"
)

// ingestForWatch ingests path the way auto-ingest does, so the watcher has a
// previous version of the file to revise.
func ingestForWatch(t *testing.T, w *Watcher, path string) []domain.Event {
	t.Helper()
	extractor, err := pipeline.NewExtractor(false)
	if err != nil {
		t.Fatalf("extractor: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	events, err := ingestSingleDoc(context.Background(), w.writer, ingest.NewService(), parser.NewNormalizer(),
		extractor, relate.NewEngine(), "initial", resolved, "")
	if err != nil {
		t.Fatalf("initial ingest: %v", err)
	}
	return events
}

// eventWithContent returns the event of events whose content is text.
func eventWithContent(t *testing.T, events []domain.Event, text string) domain.Event {
	t.Helper()
	for _, e := range events {
		if e.Content == text {
			return e
		}
	}
	t.Fatalf("no event with content %q among %d", text, len(events))
	return domain.Event{}
}

const (
	keptParagraph    = "The billing service stores invoices in PostgreSQL."
	removedParagraph = "The billing service retries failed charges five times."
)

// ADR 0036: when a watched file changes, the evidence of text that is no longer
// in it is retracted; the evidence of text still present is not.
func TestWatcher_RevisionRetractsRemovedText(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(t, dir)
	ctx := context.Background()
	path := filepath.Join(dir, "billing.md")
	writeFile(t, path, keptParagraph+"\n\n"+removedParagraph)
	initial := ingestForWatch(t, w, path)
	kept := eventWithContent(t, initial, keptParagraph)
	removed := eventWithContent(t, initial, removedParagraph)

	conn := w.writer.Conn()
	removedClaims, err := conn.Claims.ListByEventIDs(ctx, []string{removed.ID})
	if err != nil {
		t.Fatalf("claims of the removed paragraph: %v", err)
	}
	if len(removedClaims) == 0 {
		t.Fatal("fixture: the removed paragraph produced no belief, so the retraction assertion would be vacuous")
	}

	if _, err := w.Add(path); err != nil {
		t.Fatalf("Add: %v", err)
	}
	writeFile(t, path, keptParagraph+"\n\nThe billing service emails a receipt after every charge.")
	if changed, _ := w.tick(ctx); changed != 1 {
		t.Fatalf("tick changed %d files, want 1", changed)
	}

	if _, ok, err := govwrite.LookupRetraction(ctx, conn, removed.ID); err != nil || !ok {
		t.Errorf("event of the removed paragraph not retracted: ok=%v err=%v", ok, err)
	}
	if _, ok, err := govwrite.LookupRetraction(ctx, conn, kept.ID); err != nil || ok {
		t.Errorf("event of the kept paragraph was retracted: ok=%v err=%v", ok, err)
	}
	ids := make([]string, len(removedClaims))
	for i, c := range removedClaims {
		ids[i] = c.ID
	}
	after, err := conn.Claims.ListByIDs(ctx, ids)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	for _, c := range after {
		if c.Status != domain.ClaimStatusDeprecated {
			t.Errorf("belief %q from the removed paragraph is %s, want deprecated", c.Text, c.Status)
		}
	}

	// The revision's own events are live, and a second tick retracts nothing new.
	if changed, _ := w.tick(ctx); changed != 0 {
		t.Fatalf("second tick changed %d files, want 0", changed)
	}
}

// A deleted watched file retracts every event it contributed.
func TestWatcher_DeletedFileRetractsItsEvents(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(t, dir)
	ctx := context.Background()
	path := filepath.Join(dir, "gone.md")
	writeFile(t, path, keptParagraph+"\n\n"+removedParagraph)
	initial := ingestForWatch(t, w, path)
	if _, err := w.Add(path); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, removed := w.tick(ctx); removed != 1 {
		t.Fatalf("tick removed %d files, want 1", removed)
	}
	for _, e := range initial {
		if _, ok, err := govwrite.LookupRetraction(ctx, w.writer.Conn(), e.ID); err != nil || !ok {
			t.Errorf("event %s of the deleted file not retracted: ok=%v err=%v", e.ID, ok, err)
		}
	}
}
