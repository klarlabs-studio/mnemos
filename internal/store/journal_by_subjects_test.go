package store_test

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// Every backend reads the journal entries of the given kinds for many
// subjects in one query. Retraction depends on it to avoid a round trip per
// episode (ADR 0043); a backend without it falls back to that, so its absence
// is a failure here, not a skip.
func TestJournal_ListBySubjectsAcrossBackends(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, b := range openBackends(t) {
		lister, ok := b.conn.Journal.(ports.JournalSubjectsLister)
		if !ok {
			t.Errorf("%s: journal does not implement ports.JournalSubjectsLister", b.name)
			continue
		}
		entries := []domain.JournalEntry{
			{ID: "jb-1", At: at, Kind: "event_retraction", SubjectID: "ev-1", Data: "{}"},
			{ID: "jb-2", At: at.Add(time.Minute), Kind: "event_unretraction", SubjectID: "ev-1", Data: "{}"},
			{ID: "jb-3", At: at, Kind: "event_retraction", SubjectID: "ev-2", Data: "{}"},
			{ID: "jb-4", At: at, Kind: "trust_change", SubjectID: "ev-1", Data: "{}"},
			{ID: "jb-5", At: at, Kind: "event_retraction", SubjectID: "ev-3", Data: "{}"},
		}
		if err := b.conn.Journal.Append(ctx, entries); err != nil {
			t.Fatalf("%s: append: %v", b.name, err)
		}
		got, err := lister.ListBySubjects(ctx, []string{"ev-1", "ev-2", "ev-none"}, []string{"event_retraction", "event_unretraction"})
		if err != nil {
			t.Fatalf("%s: ListBySubjects: %v", b.name, err)
		}
		ids := make([]string, len(got))
		for i, e := range got {
			ids[i] = e.ID
		}
		sort.Strings(ids)
		if strings.Join(ids, " ") != "jb-1 jb-2 jb-3" {
			t.Errorf("%s: ListBySubjects = %v, want [jb-1 jb-2 jb-3]", b.name, ids)
		}
		if empty, err := lister.ListBySubjects(ctx, nil, []string{"event_retraction"}); err != nil || len(empty) != 0 {
			t.Errorf("%s: no subjects = %v, %v; want empty", b.name, empty, err)
		}
	}
}
