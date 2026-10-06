package store_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/browse"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/page"
)

// Walking the belief browse page by page with next cursors returns exactly the
// beliefs the in-process definition (browse.BeliefsInGo) matches, each once,
// with the same total on every page: the pages partition the filtered set on
// every backend, whether the store or Go did the paging. Timestamps collide in
// groups (ties broken by id) and include whole-second values, the case where
// SQLite's text order is not chronological.
func TestBrowseBeliefs_CursorPagesPartitionTheFilterAcrossBackends(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for _, b := range openBackends(t) {
		var claims []domain.Claim
		var events []domain.Event
		var links []domain.ClaimEvidence
		for i := 0; i < 90; i++ {
			at := base.Add(time.Duration(i/3) * time.Second) // groups of three share an instant
			if i%4 == 1 {
				at = at.Add(500 * time.Millisecond) // and some have a fraction
			}
			c := domain.Claim{ID: fmt.Sprintf("b%03d", i), Text: fmt.Sprintf("belief %d", i), Type: domain.ClaimTypeFact,
				Confidence: 0.6, Status: domain.ClaimStatusActive, CreatedAt: at, ValidFrom: at}
			if i%3 == 0 {
				c.Type = domain.ClaimTypeDecision
			}
			if i%5 == 0 {
				c.Status = domain.ClaimStatusContested
			}
			claims = append(claims, c)
			ev := fmt.Sprintf("bev%03d", i)
			events = append(events, domain.Event{ID: ev, RunID: fmt.Sprintf("run-%d", i%2), Content: c.Text, SchemaVersion: "v1",
				SourceInputID: "src-" + ev, Timestamp: at, IngestedAt: at})
			links = append(links, domain.ClaimEvidence{ClaimID: c.ID, EventID: ev})
		}
		for _, e := range events {
			if err := b.conn.Events.Append(ctx, e); err != nil {
				t.Fatalf("%s: %v", b.name, err)
			}
		}
		if err := b.conn.Claims.Upsert(ctx, claims); err != nil {
			t.Fatalf("%s: %v", b.name, err)
		}
		if err := b.conn.Claims.UpsertEvidence(ctx, links); err != nil {
			t.Fatalf("%s: %v", b.name, err)
		}
		for i := 0; i < 90; i += 7 { // some beliefs superseded mid-range
			if err := b.conn.Claims.SetValidity(ctx, claims[i].ID, base.Add(10*time.Second)); err != nil {
				t.Fatalf("%s: %v", b.name, err)
			}
		}

		filters := []page.ClaimFilter{
			{},
			{Type: string(domain.ClaimTypeDecision)},
			{Status: string(domain.ClaimStatusContested), Type: string(domain.ClaimTypeFact)},
			{RunID: "run-1"},
			{AsOf: base.Add(20 * time.Second)},
			{RecordedAsOf: base.Add(15*time.Second + 500*time.Millisecond), RunID: "run-0"},
		}
		for fi, f := range filters {
			want, err := browse.BeliefsInGo(ctx, b.conn, f, nil, 1000, 0)
			if err != nil {
				t.Fatal(err)
			}
			if want.Total == 0 {
				t.Fatalf("%s filter %d matches nothing; the walk would be vacuous", b.name, fi)
			}
			wantIDs := make([]string, 0, len(want.Claims))
			for _, c := range want.Claims {
				wantIDs = append(wantIDs, c.ID)
			}
			for _, size := range []int{1, 4, 7} {
				var got []string
				var after *page.Key
				for pages := 0; ; pages++ {
					if pages > 200 {
						t.Fatalf("%s filter %d size %d: no end to the pages", b.name, fi, size)
					}
					pg, err := browse.Beliefs(ctx, b.conn, f, after, size, 0)
					if err != nil {
						t.Fatalf("%s filter %d: %v", b.name, fi, err)
					}
					if pg.Total != want.Total {
						t.Fatalf("%s filter %d: page total %d, want %d", b.name, fi, pg.Total, want.Total)
					}
					if len(pg.Claims) > size {
						t.Fatalf("%s filter %d: page of %d exceeds limit %d", b.name, fi, len(pg.Claims), size)
					}
					for _, c := range pg.Claims {
						got = append(got, c.ID)
					}
					if !pg.More {
						break
					}
					last := pg.Claims[len(pg.Claims)-1]
					after = &page.Key{At: last.CreatedAt, ID: last.ID}
				}
				sortedGot, sortedWant := slices.Clone(got), slices.Clone(wantIDs)
				slices.Sort(sortedGot)
				slices.Sort(sortedWant)
				if !slices.Equal(sortedGot, sortedWant) {
					t.Fatalf("%s filter %d size %d: pages returned %v, want %v", b.name, fi, size, sortedGot, sortedWant)
				}
			}
		}
	}
}

// A belief recorded while a client pages does not repeat a belief on the next
// page. With offsets it did: every row shifted down by one.
func TestBrowseBeliefs_CursorIsStableUnderConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for _, b := range openBackends(t) {
		var claims []domain.Claim
		for i := 0; i < 10; i++ {
			at := base.Add(time.Duration(i) * time.Second)
			claims = append(claims, domain.Claim{ID: fmt.Sprintf("s%02d", i), Text: "s", Type: domain.ClaimTypeFact,
				Confidence: 0.6, Status: domain.ClaimStatusActive, CreatedAt: at, ValidFrom: at})
		}
		if err := b.conn.Claims.Upsert(ctx, claims); err != nil {
			t.Fatal(err)
		}
		first, err := browse.Beliefs(ctx, b.conn, page.ClaimFilter{}, nil, 4, 0)
		if err != nil {
			t.Fatal(err)
		}
		newer := base.Add(time.Hour)
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{ID: "s-new", Text: "s", Type: domain.ClaimTypeFact,
			Confidence: 0.6, Status: domain.ClaimStatusActive, CreatedAt: newer, ValidFrom: newer}}); err != nil {
			t.Fatal(err)
		}
		last := first.Claims[len(first.Claims)-1]
		second, err := browse.Beliefs(ctx, b.conn, page.ClaimFilter{}, &page.Key{At: last.CreatedAt, ID: last.ID}, 4, 0)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, c := range append(first.Claims, second.Claims...) {
			if seen[c.ID] {
				t.Fatalf("%s: belief %s appears on two pages after a concurrent write", b.name, c.ID)
			}
			seen[c.ID] = true
		}
		byOffset, err := browse.BeliefsInGo(ctx, b.conn, page.ClaimFilter{}, nil, 4, 4)
		if err != nil {
			t.Fatal(err)
		}
		// The failure the cursor exists to prevent: offset 4 now starts at the
		// row that ended page one.
		if byOffset.Claims[0].ID != last.ID {
			t.Fatalf("%s: offset page two starts at %s, expected the repeated %s; the premise of this test is gone", b.name, byOffset.Claims[0].ID, last.ID)
		}
	}
}
