package store_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// last_confirmed (ADR 0026) has exactly one writer, MarkConfirmed. On every
// backend it must round-trip; MarkVerified, which recall and replay call as
// rehearsal, must not touch it; and re-ingesting the claim must not erase it.
func TestLastConfirmed_HasOneWriterAcrossBackends(t *testing.T) {
	backends := openBackends(t)
	ctx := context.Background()
	created := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	confirmedAt := time.Date(2026, 9, 2, 9, 30, 0, 0, time.UTC)
	rehearsedAt := time.Date(2026, 9, 30, 9, 30, 0, 0, time.UTC)
	const id = "c-confirmation"
	claim := domain.Claim{
		ID: id, Text: "the queue drains within five minutes", Type: domain.ClaimTypeFact,
		Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: created,
	}

	read := func(t *testing.T, name string, conn interface {
		ListByIDs(context.Context, []string) ([]domain.Claim, error)
	}) domain.Claim {
		t.Helper()
		got, err := conn.ListByIDs(ctx, []string{id})
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: read back: %v (rows=%d)", name, err, len(got))
		}
		return got[0]
	}

	for _, b := range backends {
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{claim}); err != nil {
			t.Fatalf("%s: upsert: %v", b.name, err)
		}
		if got := read(t, b.name, b.conn.Claims); !got.LastConfirmed.IsZero() {
			t.Errorf("%s: a fresh claim reads LastConfirmed = %v, want the zero time", b.name, got.LastConfirmed)
		}

		if err := b.conn.Claims.MarkConfirmed(ctx, id, confirmedAt); err != nil {
			t.Fatalf("%s: mark confirmed: %v", b.name, err)
		}
		if got := read(t, b.name, b.conn.Claims); !got.LastConfirmed.Equal(confirmedAt) {
			t.Errorf("%s: LastConfirmed = %v after MarkConfirmed, want %v", b.name, got.LastConfirmed, confirmedAt)
		}

		if err := b.conn.Claims.MarkVerified(ctx, id, rehearsedAt, 0); err != nil {
			t.Fatalf("%s: mark verified: %v", b.name, err)
		}
		reingest := claim
		reingest.LastConfirmed = time.Time{}
		if err := b.conn.Claims.Upsert(ctx, []domain.Claim{reingest}); err != nil {
			t.Fatalf("%s: re-upsert: %v", b.name, err)
		}
		got := read(t, b.name, b.conn.Claims)
		if !got.LastConfirmed.Equal(confirmedAt) {
			t.Errorf("%s: LastConfirmed = %v after a rehearsal and a re-ingest, want it unchanged at %v",
				b.name, got.LastConfirmed, confirmedAt)
		}
		if !got.LastVerified.Equal(rehearsedAt) {
			t.Errorf("%s: fixture: LastVerified = %v, want the rehearsal time %v", b.name, got.LastVerified, rehearsedAt)
		}
	}
}
