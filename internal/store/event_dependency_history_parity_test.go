package store_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/recorded"
)

// ADR 0038, editing dependencies, edit history: reviving a removed pair keeps
// the interval it supersedes, with who recorded it, who removed it and why, on
// every backend. The WithRemoved reads return one row per interval, and a
// recorded-as-of read counts the dependency at T iff one interval contains T,
// so a removal followed by a revival no longer erases the time before it.
func TestEventDependencies_RevivalKeepsTheEditHistoryAcrossBackends(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	declared := base.Add(24 * time.Hour)
	removed := base.Add(48 * time.Hour)
	revived := base.Add(72 * time.Hour)
	removedAgain := base.Add(96 * time.Hour)
	for _, b := range openBackends(t) {
		for _, e := range []domain.Event{
			{ID: "hi-orig", Content: "original", SourceInputID: "src-orig", CreatedBy: "alice"},
			{ID: "hi-copy", Content: "copy", SourceInputID: "src-copy", CreatedBy: "carol"},
		} {
			e.SchemaVersion, e.Timestamp, e.IngestedAt = "v1", base, base
			if err := b.conn.Events.Append(ctx, e); err != nil {
				t.Fatalf("%s: append %s: %v", b.name, e.ID, err)
			}
		}
		deps := b.conn.Dependencies
		if _, err := deps.Declare(ctx, domain.EventDependency{EventID: "hi-copy", DependsOn: "hi-orig", Kind: domain.DependencyCopies,
			Signal: domain.SignalDeclared, Reference: "reposted", CreatedAt: declared, CreatedBy: "alice"}); err != nil {
			t.Fatalf("%s: declare: %v", b.name, err)
		}
		tomb, err := deps.Remove(ctx, "hi-copy", "hi-orig", "bob", "it only quotes the title", removed)
		if err != nil {
			t.Fatalf("%s: remove: %v", b.name, err)
		}
		if tomb.RemovalReason != "it only quotes the title" || tomb.RemovedBy != "bob" {
			t.Errorf("%s: removed row = %+v, want bob's reason", b.name, tomb)
		}
		got, err := deps.Declare(ctx, domain.EventDependency{EventID: "hi-copy", DependsOn: "hi-orig", Kind: domain.DependencyDerivedFrom,
			Signal: domain.SignalDeclared, Reference: "a summary after all", CreatedAt: revived, CreatedBy: "carol"})
		if err != nil {
			t.Fatalf("%s: revive: %v", b.name, err)
		}
		if !got.Live() || got.RemovalReason != "" || got.RemovedBy != "" || !got.CreatedAt.Equal(revived) {
			t.Errorf("%s: revived row = %+v, want a fresh live interval", b.name, got)
		}

		// The superseded interval is kept exactly as it was.
		all, err := deps.ListByEventIDsWithRemoved(ctx, []string{"hi-copy"})
		if err != nil || len(all) != 2 {
			t.Fatalf("%s: intervals = %v (%v), want the removed one and the live one", b.name, depPairs(all), err)
		}
		byStart := map[time.Time]domain.EventDependency{}
		for _, d := range all {
			byStart[d.CreatedAt.UTC()] = d
		}
		old, ok := byStart[declared]
		if !ok || old.Kind != domain.DependencyCopies || old.Reference != "reposted" || old.CreatedBy != "alice" ||
			old.RemovedAt == nil || !old.RemovedAt.Equal(removed) || old.RemovedBy != "bob" || old.RemovalReason != "it only quotes the title" {
			t.Errorf("%s: kept interval = %+v, want alice's copy removed by bob with his reason", b.name, old)
		}
		if cur, ok := byStart[revived]; !ok || !cur.Live() || cur.Kind != domain.DependencyDerivedFrom || cur.CreatedBy != "carol" {
			t.Errorf("%s: current interval = %+v", b.name, cur)
		}
		if in, err := deps.ListDependentsWithRemoved(ctx, []string{"hi-orig"}); err != nil || len(in) != 2 {
			t.Errorf("%s: dependents with removed = %v (%v), want both intervals", b.name, depPairs(in), err)
		}
		if live, err := deps.ListByEventIDs(ctx, []string{"hi-copy"}); err != nil || len(live) != 1 || !live[0].CreatedAt.Equal(revived) {
			t.Errorf("%s: live = %v (%v), want only the current interval", b.name, depPairs(live), err)
		}

		// A second removal and revival add a second closed interval.
		if _, err := deps.Remove(ctx, "hi-copy", "hi-orig", "dave", "", removedAgain); err != nil {
			t.Fatalf("%s: remove again: %v", b.name, err)
		}
		if _, err := deps.Declare(ctx, domain.EventDependency{EventID: "hi-copy", DependsOn: "hi-orig", Kind: domain.DependencyCites,
			Signal: domain.SignalDeclared, CreatedAt: removedAgain.Add(24 * time.Hour), CreatedBy: "erin"}); err != nil {
			t.Fatalf("%s: revive again: %v", b.name, err)
		}
		all, err = deps.ListByEventIDsWithRemoved(ctx, []string{"hi-copy"})
		if err != nil || len(all) != 3 {
			t.Fatalf("%s: intervals after a second revival = %v (%v), want 3", b.name, depPairs(all), err)
		}

		// Recorded as of T: in force iff an interval contains T.
		in := func(at time.Time) bool {
			for _, d := range all {
				if d.LiveAt(at) {
					return true
				}
			}
			return false
		}
		for _, tc := range []struct {
			label string
			at    time.Time
			want  bool
		}{
			{"before the first declaration", declared.Add(-time.Hour), false},
			{"inside the first interval", declared.Add(time.Hour), true},
			{"after the first removal", removed.Add(time.Hour), false},
			{"inside the second interval", revived.Add(time.Hour), true},
			{"after the second removal", removedAgain.Add(time.Hour), false},
			{"inside the current interval", removedAgain.Add(25 * time.Hour), true},
		} {
			if got := in(tc.at); got != tc.want {
				t.Errorf("%s: in force %s = %v, want %v", b.name, tc.label, got, tc.want)
			}
		}

		// Trust as of the first interval counts the copy as a repeat, as it did
		// before the revival rewrote the current row.
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{ID: "hi-belief", Text: "kept", Type: domain.ClaimTypeFact,
			Confidence: 0.7, Status: domain.ClaimStatusActive, CreatedAt: base}}); err != nil {
			t.Fatalf("%s: upsert: %v", b.name, err)
		}
		if err := b.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
			{ClaimID: "hi-belief", EventID: "hi-orig", CreatedAt: base},
			{ClaimID: "hi-belief", EventID: "hi-copy", CreatedAt: base},
		}); err != nil {
			t.Fatalf("%s: evidence: %v", b.name, err)
		}
		links, err := b.conn.Claims.ListEvidenceByClaimIDs(ctx, []string{"hi-belief"})
		if err != nil {
			t.Fatalf("%s: links: %v", b.name, err)
		}
		evs, err := b.conn.Events.ListByIDs(ctx, []string{"hi-orig", "hi-copy"})
		if err != nil {
			t.Fatalf("%s: events: %v", b.name, err)
		}
		byID := map[string]domain.Event{}
		for _, e := range evs {
			byID[e.ID] = e
		}
		claim := domain.Claim{ID: "hi-belief", Confidence: 0.7, CreatedAt: base}
		dependent, independent := domain.EffectiveEvidenceCount(1, 2), domain.EffectiveEvidenceCount(2, 2)
		for _, tc := range []struct {
			label string
			at    time.Time
			want  int
		}{
			{"inside the first interval", declared.Add(time.Hour), dependent},
			{"between the intervals", removed.Add(time.Hour), independent},
			{"inside the second interval", revived.Add(time.Hour), dependent},
		} {
			if got := recorded.TrustInputAt(claim, links, byID, all, nil, nil, tc.at).EvidenceCount; got != tc.want {
				t.Errorf("%s: recorded evidence count %s = %d, want %d", b.name, tc.label, got, tc.want)
			}
		}

		// Destroying an episode hard-deletes its whole history too.
		if err := b.conn.Events.DeleteByID(ctx, "hi-orig"); err != nil {
			t.Fatalf("%s: delete hi-orig: %v", b.name, err)
		}
		if left, err := deps.ListByEventIDsWithRemoved(ctx, []string{"hi-copy"}); err != nil || len(left) != 0 {
			t.Errorf("%s: intervals after destroying the source = %v (%v), want none", b.name, depPairs(left), err)
		}
	}
}
