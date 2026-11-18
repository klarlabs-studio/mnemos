package store_test

import (
	"context"
	"math"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/recorded"
	"go.klarlabs.de/mnemos/internal/trust"
)

// ADR 0036 §7: every write that changes a trust input the claims row stores
// itself (confidence, half_life_days, last_confirmed) records the values it
// replaced, on every backend, and a write that changes none of them records
// nothing. Asserted on what ListTrustInputHistory RETURNS.
//
// The writers are Upsert (confidence; half-life when non-zero, per the
// conflict rule), MarkVerified (half-life when non-zero) and MarkConfirmed.
// TestTrustInputWriters_RecordHistory proves statically that no other
// statement writes those columns.
func TestTrustInputHistory_RecordsEveryWritePath(t *testing.T) {
	ctx := context.Background()
	created := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)

	for _, b := range openBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			lister, ok := b.conn.Claims.(ports.TrustInputHistoryLister)
			if !ok {
				t.Fatalf("%s claims do not implement TrustInputHistoryLister: its confidence, half-life and confirmation writes are unversioned", b.name)
			}
			upsert := func(confidence, halfLife float64) {
				t.Helper()
				if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{
					ID: "c-inputs", Text: "the pool is resized on deploy", Type: domain.ClaimTypeFact,
					Confidence: confidence, HalfLifeDays: halfLife, Status: domain.ClaimStatusActive, CreatedAt: created,
				}}); err != nil {
					t.Fatalf("upsert: %v", err)
				}
			}
			confirmedMon := created.Add(time.Hour)
			confirmedThu := created.Add(72 * time.Hour)

			upsert(0.6, 0)  // insert: no prior values
			upsert(0.6, 0)  // unchanged: nothing
			upsert(0.8, 0)  // confidence 0.6 -> 0.8
			upsert(0.8, 14) // half-life 0 -> 14
			upsert(0.8, 0)  // zero preserves the stored half-life: nothing
			if err := b.conn.Claims.MarkVerified(ctx, "c-inputs", time.Time{}, 0); err != nil {
				t.Fatalf("verify: %v", err) // rehearsal is not a trust input: nothing
			}
			if err := b.conn.Claims.MarkVerified(ctx, "c-inputs", time.Time{}, 30); err != nil {
				t.Fatalf("verify with half-life: %v", err) // half-life 14 -> 30
			}
			if err := b.conn.Claims.MarkConfirmed(ctx, "c-inputs", confirmedMon); err != nil {
				t.Fatalf("confirm: %v", err) // never confirmed -> Monday
			}
			if err := b.conn.Claims.MarkConfirmed(ctx, "c-inputs", confirmedThu); err != nil {
				t.Fatalf("confirm again: %v", err) // Monday -> Thursday
			}
			if w, ok := b.conn.Claims.(ports.BeliefCreditWriter); ok {
				if err := w.ApplyBeliefCredit(ctx, "c-inputs", map[string]float64{domain.CreditAppliedComponentKey: 0.1}, 0.8); err != nil {
					t.Fatalf("credit: %v", err) // credit has its own history: nothing here
				}
			}

			history, err := lister.ListTrustInputHistory(ctx, []string{"c-inputs", "c-missing"})
			if err != nil {
				t.Fatalf("list trust input history: %v", err)
			}
			want := []domain.TrustInputChange{
				{Confidence: 0.6},
				{Confidence: 0.8},
				{Confidence: 0.8, HalfLifeDays: 14},
				{Confidence: 0.8, HalfLifeDays: 30},
				{Confidence: 0.8, HalfLifeDays: 30, LastConfirmed: confirmedMon},
			}
			if len(history) != len(want) {
				t.Fatalf("trust input history = %+v, want %d changes", history, len(want))
			}
			for i, w := range want {
				h := history[i]
				if h.ClaimID != "c-inputs" || h.Confidence != w.Confidence || h.HalfLifeDays != w.HalfLifeDays ||
					!h.LastConfirmed.Equal(w.LastConfirmed) || h.ChangedAt.IsZero() {
					t.Errorf("change %d = %+v, want prior values %+v with a time", i, h, w)
				}
				if i > 0 && h.ChangedAt.Before(history[i-1].ChangedAt) {
					t.Errorf("trust input history is not oldest first: %+v", history)
				}
			}

			if err := b.conn.Claims.DeleteCascade(ctx, "c-inputs"); err != nil {
				t.Fatalf("delete cascade: %v", err)
			}
			if left, err := lister.ListTrustInputHistory(ctx, []string{"c-inputs"}); err != nil || len(left) != 0 {
				t.Errorf("trust input history after DeleteCascade = %+v (%v), want none", left, err)
			}
		})
	}
}

// ADR 0036 §7, end to end on every backend: confidence, half-life and an
// explicit confirmation changed after T do not change the trust a
// recorded-as-of read reports at T. The belief was confirmed both before and
// after T, so at T it reads as confirmed at the earlier confirmation, not as
// unconfirmed.
func TestRecordedAsOf_StoredTrustInputsAreReconstructedAtT(t *testing.T) {
	ctx := context.Background()

	for _, b := range openBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			// MySQL keeps microseconds; keep every instant representable.
			now := func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
			between := func() time.Time {
				time.Sleep(5 * time.Millisecond)
				at := now()
				time.Sleep(5 * time.Millisecond)
				return at
			}

			created := now().Add(-time.Hour)
			if err := b.conn.Events.Append(ctx, domain.Event{
				ID: "ev-at", Content: "the pool is resized on deploy", SchemaVersion: "v1", SourceInputID: "src-at",
				Timestamp: created, IngestedAt: created, CreatedBy: "tester",
			}); err != nil {
				t.Fatalf("append: %v", err)
			}
			if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{
				ID: "c-at", Text: "the pool is resized on deploy", Type: domain.ClaimTypeFact,
				Confidence: 0.6, HalfLifeDays: 14, Status: domain.ClaimStatusActive, CreatedAt: created,
			}}); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			if err := b.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: "c-at", EventID: "ev-at", CreatedAt: created}}); err != nil {
				t.Fatalf("link: %v", err)
			}
			firstConfirmed := now()
			if err := b.conn.Claims.MarkConfirmed(ctx, "c-at", firstConfirmed); err != nil {
				t.Fatalf("confirm: %v", err)
			}
			at := between()

			// After T: more confidence, a longer half-life, a fresh confirmation.
			if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{
				ID: "c-at", Text: "the pool is resized on deploy", Type: domain.ClaimTypeFact,
				Confidence: 0.95, Status: domain.ClaimStatusActive, CreatedAt: created,
			}}); err != nil {
				t.Fatalf("raise confidence: %v", err)
			}
			if err := b.conn.Claims.MarkVerified(ctx, "c-at", time.Time{}, 365); err != nil {
				t.Fatalf("verify: %v", err)
			}
			laterConfirmed := now()
			if err := b.conn.Claims.MarkConfirmed(ctx, "c-at", laterConfirmed); err != nil {
				t.Fatalf("confirm again: %v", err)
			}
			after := between()

			readAt := func(t0 time.Time) domain.Claim {
				t.Helper()
				view := recorded.Claims{
					ClaimRepository: b.conn.Claims,
					Events:          recorded.Events{EventRepository: b.conn.Events, At: t0},
					At:              t0,
				}
				got, err := view.ListByIDs(ctx, []string{"c-at"})
				if err != nil || len(got) != 1 {
					t.Fatalf("recorded read at %v: %v (%d claims)", t0, err, len(got))
				}
				return got[0]
			}
			input := func(confidence, halfLife float64, confirmed time.Time) domain.TrustInput {
				return domain.TrustInput{
					Confidence: confidence, HalfLifeDays: halfLife, LastConfirmed: confirmed,
					EvidenceCount: 1, LatestEvidence: created,
				}
			}

			held := readAt(at)
			if held.Confidence != 0.6 || held.HalfLifeDays != 14 || !held.LastConfirmed.Equal(firstConfirmed) {
				t.Errorf("belief at T reads confidence %v, half-life %v, confirmed %v; want 0.6, 14, %v",
					held.Confidence, held.HalfLifeDays, held.LastConfirmed, firstConfirmed)
			}
			then, want := held.TrustScore, trust.At(input(0.6, 14, firstConfirmed), at)
			if math.Abs(then-want) > 1e-6 {
				t.Errorf("trust at T = %.6f, want %.6f (confidence 0.6, half-life 14, confirmed at the first confirmation)", then, want)
			}
			later, wantLater := readAt(after).TrustScore, trust.At(input(0.95, 365, laterConfirmed), after)
			if math.Abs(later-wantLater) > 1e-6 {
				t.Errorf("trust after the changes = %.6f, want %.6f (confidence 0.95, half-life 365, confirmed again)", later, wantLater)
			}
			if math.Abs(then-later) < 1e-6 {
				t.Errorf("trust at T equals trust after the changes (%.6f): the later writes leaked into the past", later)
			}
		})
	}
}
