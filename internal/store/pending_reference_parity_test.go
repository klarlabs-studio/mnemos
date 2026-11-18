package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ADR 0038, late citations: pending references are stored, listed by key,
// deleted by (episode, key) and destroyed with their episode the same way on
// every backend. Asserted on what each store returns.
func TestPendingReferences_AgreeAcrossBackends(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 18, 9, 30, 15, 123456000, time.UTC)
	for _, b := range openBackends(t) {
		deps := b.conn.Dependencies
		for _, e := range []domain.Event{
			{ID: "pend-a", Content: "Source: the vendor advisory", SourceInputID: "blog", SchemaVersion: "v1", Timestamp: at, IngestedAt: at},
			{ID: "pend-b", Content: "see https://x.example/a", SourceInputID: "digest", SchemaVersion: "v1", Timestamp: at, IngestedAt: at},
		} {
			if err := b.conn.Events.Append(ctx, e); err != nil {
				t.Fatalf("%s: append %s: %v", b.name, e.ID, err)
			}
		}
		rows := []domain.PendingReference{
			{EventID: "pend-a", LookupKey: "vendor advisory", CreatedAt: at},
			{EventID: "pend-a", LookupKey: "advisory", CreatedAt: at},
			{EventID: "pend-b", LookupKey: "vendor advisory", CreatedAt: at.Add(time.Second)},
			{EventID: "pend-b", LookupKey: "https: x example a", CreatedAt: at.Add(time.Second)},
		}
		if err := deps.AppendPending(ctx, rows); err != nil {
			t.Fatalf("%s: append pending: %v", b.name, err)
		}
		// A repeat is ignored: the first row stands.
		if err := deps.AppendPending(ctx, []domain.PendingReference{{EventID: "pend-a", LookupKey: "vendor advisory", CreatedAt: at.Add(time.Hour)}}); err != nil {
			t.Fatalf("%s: repeat append: %v", b.name, err)
		}
		if err := deps.AppendPending(ctx, []domain.PendingReference{{EventID: "pend-a", LookupKey: " "}}); err == nil {
			t.Errorf("%s: a pending reference with no key was accepted", b.name)
		}

		got, err := deps.ListPendingByKeys(ctx, []string{"vendor advisory", "no such key"})
		if err != nil {
			t.Fatalf("%s: list pending: %v", b.name, err)
		}
		if s := pendingRows(got); s != "[pend-a/vendor advisory pend-b/vendor advisory]" {
			t.Errorf("%s: pending under %q = %s, want both episodes oldest first", b.name, "vendor advisory", s)
		}
		if !got[0].CreatedAt.Equal(at) {
			t.Errorf("%s: stored created_at = %v, want %v (the first row stands)", b.name, got[0].CreatedAt, at)
		}
		if none, err := deps.ListPendingByKeys(ctx, nil); err != nil || len(none) != 0 {
			t.Errorf("%s: no keys listed %v (%v), want none", b.name, none, err)
		}

		// Delete is by (episode, key): the other episode's row under the same
		// key survives, and deleting a row that is not stored is no error.
		if err := deps.DeletePending(ctx, []domain.PendingReference{
			{EventID: "pend-a", LookupKey: "vendor advisory"},
			{EventID: "pend-a", LookupKey: "never stored"},
		}); err != nil {
			t.Fatalf("%s: delete pending: %v", b.name, err)
		}
		got, err = deps.ListPendingByKeys(ctx, []string{"vendor advisory", "advisory"})
		if err != nil {
			t.Fatalf("%s: list pending: %v", b.name, err)
		}
		if s := pendingRows(got); s != "[pend-a/advisory pend-b/vendor advisory]" {
			t.Errorf("%s: pending after delete = %s", b.name, s)
		}

		// Destroying an episode removes its pending rows; DeleteAll removes all.
		if err := b.conn.Events.DeleteByID(ctx, "pend-a"); err != nil {
			t.Fatalf("%s: delete pend-a: %v", b.name, err)
		}
		got, err = deps.ListPendingByKeys(ctx, []string{"vendor advisory", "advisory", "https: x example a"})
		if err != nil {
			t.Fatalf("%s: list pending: %v", b.name, err)
		}
		if s := pendingRows(got); s != "[pend-b/https: x example a pend-b/vendor advisory]" {
			t.Errorf("%s: pending after deleting pend-a = %s", b.name, s)
		}
		if err := b.conn.Claims.DeleteAll(ctx); err != nil {
			t.Fatalf("%s: delete claims: %v", b.name, err)
		}
		if err := b.conn.Events.DeleteAll(ctx); err != nil {
			t.Fatalf("%s: delete all events: %v", b.name, err)
		}
		if left, err := deps.ListPendingByKeys(ctx, []string{"vendor advisory", "https: x example a"}); err != nil || len(left) != 0 {
			t.Errorf("%s: pending after DeleteAll = %s (%v), want none", b.name, pendingRows(left), err)
		}
	}
}

func pendingRows(refs []domain.PendingReference) string {
	out := make([]string, len(refs))
	for i, p := range refs {
		out[i] = p.EventID + "/" + p.LookupKey
	}
	return fmt.Sprint(out)
}
