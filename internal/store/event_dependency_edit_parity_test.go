package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/recorded"
)

// ADR 0038, editing dependencies: declaring, removing and reviving a
// dependency behave the same on every backend, asserted on what each store
// returns. A removal is a tombstone: live reads drop it, the WithRemoved reads
// keep it, and an automatic Append of the same pair does not bring it back.
func TestEventDependencies_DeclareAndRemoveAgreeAcrossBackends(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 19, 8, 0, 0, 250000000, time.UTC)
	for _, b := range openBackends(t) {
		deps := b.conn.Dependencies
		for _, e := range []domain.Event{
			{ID: "de-src", Content: "advisory", SourceInputID: "advisory"},
			{ID: "de-copy", Content: "copy", SourceInputID: "blog"},
		} {
			e.SchemaVersion, e.Timestamp, e.IngestedAt = "v1", at, at
			if err := b.conn.Events.Append(ctx, e); err != nil {
				t.Fatalf("%s: append %s: %v", b.name, e.ID, err)
			}
		}

		declared := domain.EventDependency{EventID: "de-copy", DependsOn: "de-src", Kind: domain.DependencyCopies,
			Signal: domain.SignalDeclared, Reference: "reposted verbatim", CreatedAt: at, CreatedBy: "alice"}
		got, err := deps.Declare(ctx, declared)
		if err != nil {
			t.Fatalf("%s: declare: %v", b.name, err)
		}
		if got.Signal != domain.SignalDeclared || got.Reference != declared.Reference || got.CreatedBy != "alice" ||
			!got.CreatedAt.Equal(at) || !got.Live() || got.RemovedBy != "" {
			t.Errorf("%s: declared row = %+v", b.name, got)
		}
		if _, err := deps.Declare(ctx, declared); !errors.Is(err, domain.ErrDependencyExists) {
			t.Errorf("%s: declaring a live pair again: err = %v, want ErrDependencyExists", b.name, err)
		}

		removedAt := at.Add(time.Hour)
		removed, err := deps.Remove(ctx, "de-copy", "de-src", "bob", "", removedAt)
		if err != nil {
			t.Fatalf("%s: remove: %v", b.name, err)
		}
		if removed.RemovedAt == nil || !removed.RemovedAt.Equal(removedAt) || removed.RemovedBy != "bob" || removed.CreatedBy != "alice" {
			t.Errorf("%s: removed row = %+v", b.name, removed)
		}
		if _, err := deps.Remove(ctx, "de-copy", "de-src", "bob", "", removedAt); !errors.Is(err, domain.ErrDependencyNotFound) {
			t.Errorf("%s: removing again: err = %v, want ErrDependencyNotFound", b.name, err)
		}
		if _, err := deps.Remove(ctx, "de-src", "de-copy", "bob", "", removedAt); !errors.Is(err, domain.ErrDependencyNotFound) {
			t.Errorf("%s: removing a pair never stored: err = %v, want ErrDependencyNotFound", b.name, err)
		}

		// Detection writing the same pair again does not resurrect it.
		if err := deps.Append(ctx, []domain.EventDependency{{EventID: "de-copy", DependsOn: "de-src", Kind: domain.DependencyCites,
			Signal: domain.SignalLink, Reference: "https://advisory", CreatedAt: at.Add(2 * time.Hour), CreatedBy: domain.SystemUser}}); err != nil {
			t.Fatalf("%s: append over tombstone: %v", b.name, err)
		}
		assertLive := func(label string, wantOut, wantIn int) {
			t.Helper()
			out, err := deps.ListByEventIDs(ctx, []string{"de-copy"})
			if err != nil || len(out) != wantOut {
				t.Errorf("%s: %s: live outgoing = %v (%v), want %d", b.name, label, depPairs(out), err, wantOut)
			}
			in, err := deps.ListDependents(ctx, []string{"de-src"})
			if err != nil || len(in) != wantIn {
				t.Errorf("%s: %s: live dependents = %v (%v), want %d", b.name, label, depPairs(in), err, wantIn)
			}
		}
		assertLive("after removal and re-detection", 0, 0)

		all, err := deps.ListByEventIDsWithRemoved(ctx, []string{"de-copy"})
		if err != nil || len(all) != 1 {
			t.Fatalf("%s: outgoing with removed = %v (%v), want the tombstone", b.name, depPairs(all), err)
		}
		if tomb := all[0]; tomb.Signal != domain.SignalDeclared || tomb.RemovedAt == nil || tomb.RemovedBy != "bob" {
			t.Errorf("%s: tombstone = %+v, want the declared row, removed by bob", b.name, tomb)
		}
		if in, err := deps.ListDependentsWithRemoved(ctx, []string{"de-src"}); err != nil || len(in) != 1 || in[0].RemovedAt == nil {
			t.Errorf("%s: dependents with removed = %+v (%v)", b.name, in, err)
		}

		// A declaration revives the tombstone with its own justification.
		revivedAt := at.Add(3 * time.Hour)
		revived, err := deps.Declare(ctx, domain.EventDependency{EventID: "de-copy", DependsOn: "de-src", Kind: domain.DependencyDerivedFrom,
			Signal: domain.SignalDeclared, Reference: "it is a summary after all", CreatedAt: revivedAt, CreatedBy: "carol"})
		if err != nil {
			t.Fatalf("%s: revive: %v", b.name, err)
		}
		if revived.Kind != domain.DependencyDerivedFrom || revived.Reference != "it is a summary after all" || revived.CreatedBy != "carol" ||
			!revived.CreatedAt.Equal(revivedAt) || !revived.Live() || revived.RemovedBy != "" {
			t.Errorf("%s: revived row = %+v", b.name, revived)
		}
		assertLive("after revival", 1, 1)

		// Destroying an episode still hard-deletes its rows, tombstones too.
		if _, err := deps.Remove(ctx, "de-copy", "de-src", "bob", "", revivedAt.Add(time.Minute)); err != nil {
			t.Fatalf("%s: remove before destroy: %v", b.name, err)
		}
		if err := b.conn.Events.DeleteByID(ctx, "de-copy"); err != nil {
			t.Fatalf("%s: delete de-copy: %v", b.name, err)
		}
		if left, err := deps.ListDependentsWithRemoved(ctx, []string{"de-src"}); err != nil || len(left) != 0 {
			t.Errorf("%s: rows after destroying de-copy = %v (%v), want none", b.name, depPairs(left), err)
		}
	}
}

// Pending citations are listed by the citing episode the same way everywhere.
func TestPendingReferences_ListByEventIDsAgreesAcrossBackends(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	for _, b := range openBackends(t) {
		if err := b.conn.Dependencies.AppendPending(ctx, []domain.PendingReference{
			{EventID: "pe-a", LookupKey: "url:https://x/2", CreatedAt: at.Add(time.Second)},
			{EventID: "pe-a", LookupKey: "url:https://x/1", CreatedAt: at},
			{EventID: "pe-b", LookupKey: "url:https://x/1", CreatedAt: at},
		}); err != nil {
			t.Fatalf("%s: append pending: %v", b.name, err)
		}
		got, err := b.conn.Dependencies.ListPendingByEventIDs(ctx, []string{"pe-a"})
		if err != nil {
			t.Fatalf("%s: list pending: %v", b.name, err)
		}
		var keys []string
		for _, p := range got {
			keys = append(keys, p.EventID+" "+p.LookupKey)
		}
		if fmt.Sprint(keys) != "[pe-a url:https://x/1 pe-a url:https://x/2]" {
			t.Errorf("%s: pending of pe-a = %v", b.name, keys)
		}
		if none, err := b.conn.Dependencies.ListPendingByEventIDs(ctx, nil); err != nil || len(none) != 0 {
			t.Errorf("%s: no ids = %v (%v), want none", b.name, none, err)
		}
	}
}

// Trust follows the edits on every backend's trust path: removing the
// dependency gives the copy its author back, declaring one takes it away, and a
// recorded-as-of read counts the dependency only while it was in force.
func TestTrustInput_FollowsDependencyEditsAcrossBackends(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, b := range openBackends(t) {
		for _, e := range []domain.Event{
			{ID: "te-orig", Content: "original", SourceInputID: "src-orig", CreatedBy: "alice"},
			{ID: "te-copy", Content: "copy", SourceInputID: "src-copy", CreatedBy: "carol"},
		} {
			e.SchemaVersion, e.Timestamp, e.IngestedAt = "v1", at.Add(-72*time.Hour), at.Add(-72*time.Hour)
			if err := b.conn.Events.Append(ctx, e); err != nil {
				t.Fatalf("%s: append %s: %v", b.name, e.ID, err)
			}
		}
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{ID: "te-belief", Text: "edited", Type: domain.ClaimTypeFact,
			Confidence: 0.7, Status: domain.ClaimStatusActive, CreatedAt: at.Add(-72 * time.Hour)}}); err != nil {
			t.Fatalf("%s: upsert: %v", b.name, err)
		}
		if err := b.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
			{ClaimID: "te-belief", EventID: "te-orig", CreatedAt: at.Add(-72 * time.Hour)},
			{ClaimID: "te-belief", EventID: "te-copy", CreatedAt: at.Add(-72 * time.Hour)},
		}); err != nil {
			t.Fatalf("%s: evidence: %v", b.name, err)
		}
		lister, ok := b.conn.Claims.(ports.TrustInputLister)
		if !ok {
			t.Fatalf("%s: not a TrustInputLister", b.name)
		}
		count := func(label string, want int) {
			t.Helper()
			inputs, err := lister.ListTrustInputs(ctx, []string{"te-belief"})
			if err != nil {
				t.Fatalf("%s: trust inputs: %v", b.name, err)
			}
			if got := inputs["te-belief"].EvidenceCount; got != want {
				t.Errorf("%s: %s: evidence count = %d, want %d", b.name, label, got, want)
			}
		}
		independent, dependent := domain.EffectiveEvidenceCount(2, 2), domain.EffectiveEvidenceCount(1, 2)

		count("before any dependency", independent)
		declaredAt := at.Add(-48 * time.Hour)
		if _, err := b.conn.Dependencies.Declare(ctx, domain.EventDependency{EventID: "te-copy", DependsOn: "te-orig",
			Kind: domain.DependencyCopies, Signal: domain.SignalDeclared, CreatedAt: declaredAt, CreatedBy: "alice"}); err != nil {
			t.Fatalf("%s: declare: %v", b.name, err)
		}
		count("after declaring", dependent)
		removedAt := at.Add(-24 * time.Hour)
		if _, err := b.conn.Dependencies.Remove(ctx, "te-copy", "te-orig", "bob", "", removedAt); err != nil {
			t.Fatalf("%s: remove: %v", b.name, err)
		}
		count("after removing", independent)

		for _, tc := range []struct {
			label string
			at    time.Time
			want  int
		}{
			{"before the declaration", declaredAt.Add(-time.Hour), independent},
			{"while it was in force", declaredAt.Add(time.Hour), dependent},
			{"after its removal", removedAt.Add(time.Hour), independent},
		} {
			links, err := b.conn.Claims.ListEvidenceByClaimIDs(ctx, []string{"te-belief"})
			if err != nil {
				t.Fatalf("%s: evidence: %v", b.name, err)
			}
			evs, err := b.conn.Events.ListByIDs(ctx, []string{"te-orig", "te-copy"})
			if err != nil {
				t.Fatalf("%s: events: %v", b.name, err)
			}
			byID := map[string]domain.Event{}
			for _, e := range evs {
				byID[e.ID] = e
			}
			withRemoved, err := b.conn.Dependencies.ListByEventIDsWithRemoved(ctx, []string{"te-orig", "te-copy"})
			if err != nil {
				t.Fatalf("%s: dependencies: %v", b.name, err)
			}
			claim := domain.Claim{ID: "te-belief", Confidence: 0.7, CreatedAt: at.Add(-72 * time.Hour)}
			in := recorded.TrustInputAt(claim, links, byID, withRemoved, nil, nil, tc.at)
			if in.EvidenceCount != tc.want {
				t.Errorf("%s: recorded %s: evidence count = %d, want %d", b.name, tc.label, in.EvidenceCount, tc.want)
			}
		}
	}
}
