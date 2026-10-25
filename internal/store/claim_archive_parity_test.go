package store_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Archiving is the retrieval axis of ADR 0034. Every backend must persist
// archived_at, return it on read, leave valid time and status alone, keep it
// through an upsert (the upsert never owns it), and clear it only for the ids
// Unarchive names that are archived.
func TestClaims_ArchiveRoundTripsOnEveryBackend(t *testing.T) {
	ctx := context.Background()
	created := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	archivedAt := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)

	for _, b := range openBackends(t) {
		claims := []domain.Claim{
			{ID: "c-arch-a", Text: "the search index rebuilds nightly", Type: domain.ClaimTypeFact,
				Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: created},
			{ID: "c-arch-b", Text: "billing retries failed charges three times", Type: domain.ClaimTypeFact,
				Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: created},
		}
		if err := b.conn.Claims.Upsert(ctx, claims); err != nil {
			t.Fatalf("%s: upsert: %v", b.name, err)
		}
		if err := b.conn.Claims.Archive(ctx, "c-arch-a", archivedAt); err != nil {
			t.Fatalf("%s: archive: %v", b.name, err)
		}

		got := readClaim(t, b, "c-arch-a")
		if !got.ArchivedAt.Equal(archivedAt) {
			t.Fatalf("%s: archived_at read back as %v, want %v", b.name, got.ArchivedAt, archivedAt)
		}
		if !got.ValidTo.IsZero() || got.Status != domain.ClaimStatusActive {
			t.Fatalf("%s: archiving touched valid_to (%v) or status (%s)", b.name, got.ValidTo, got.Status)
		}

		if err := b.conn.Claims.Upsert(ctx, claims[:1]); err != nil {
			t.Fatalf("%s: re-upsert: %v", b.name, err)
		}
		if got := readClaim(t, b, "c-arch-a"); !got.ArchivedAt.Equal(archivedAt) {
			t.Fatalf("%s: an upsert changed archived_at to %v", b.name, got.ArchivedAt)
		}

		n, err := b.conn.Claims.Unarchive(ctx, []string{"c-arch-a", "c-arch-b", "c-arch-missing"})
		if err != nil {
			t.Fatalf("%s: unarchive: %v", b.name, err)
		}
		if n != 1 {
			t.Fatalf("%s: unarchive reported %d, want 1 (only c-arch-a was archived)", b.name, n)
		}
		if got := readClaim(t, b, "c-arch-a"); got.IsArchived() {
			t.Fatalf("%s: still archived after Unarchive (%v)", b.name, got.ArchivedAt)
		}
		if n, err := b.conn.Claims.Unarchive(ctx, nil); err != nil || n != 0 {
			t.Fatalf("%s: unarchive of nothing = %d, %v", b.name, n, err)
		}
	}
}

func readClaim(t *testing.T, b backendConn, id string) domain.Claim {
	t.Helper()
	got, err := b.conn.Claims.ListByIDs(context.Background(), []string{id})
	if err != nil || len(got) != 1 {
		t.Fatalf("%s: read %s: %v (%d rows)", b.name, id, err, len(got))
	}
	return got[0]
}
