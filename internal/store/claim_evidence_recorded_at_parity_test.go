package store_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// ADR 0036 step 3: an evidence link carries when it was recorded, on every
// backend, and that time survives the reads trust-at-T depends on. Asserted on
// what the store RETURNS, never on what the write was handed.
//
//   - a link written with a time keeps it;
//   - a link written without one is stamped with the store's clock;
//   - re-upserting an existing link keeps the original time;
//   - a merge (RepointEvidence) moves the link with its time;
//   - ListEvidenceByClaimIDs and ListAllEvidence agree.
func TestClaimEvidence_RecordedAtRoundTripsOnEveryBackend(t *testing.T) {
	ctx := context.Background()
	monday := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
	thursday := monday.Add(72 * time.Hour)

	for _, b := range openBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			for _, id := range []string{"ev-rec-a", "ev-rec-b"} {
				if err := b.conn.Events.Append(ctx, domain.Event{
					ID: id, Content: "source " + id, SchemaVersion: "v1", SourceInputID: "src-" + id,
					Timestamp: monday, IngestedAt: monday, CreatedBy: "tester",
				}); err != nil {
					t.Fatalf("append %s: %v", id, err)
				}
			}
			for _, id := range []string{"c-rec", "c-rec-winner"} {
				if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{
					ID: id, Text: "the pool is resized on deploy " + id, Type: domain.ClaimTypeFact,
					Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: monday,
				}}); err != nil {
					t.Fatalf("upsert %s: %v", id, err)
				}
			}

			before := time.Now().UTC().Add(-time.Second)
			if err := b.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
				{ClaimID: "c-rec", EventID: "ev-rec-a", CreatedAt: thursday},
				{ClaimID: "c-rec", EventID: "ev-rec-b"},
			}); err != nil {
				t.Fatalf("link: %v", err)
			}
			after := time.Now().UTC().Add(time.Second)
			// A re-upsert must not move the recorded time.
			if err := b.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
				{ClaimID: "c-rec", EventID: "ev-rec-a", CreatedAt: thursday.Add(time.Hour)},
			}); err != nil {
				t.Fatalf("relink: %v", err)
			}

			check := func(label string, links []domain.ClaimEvidence, claimID string) {
				t.Helper()
				got := map[string]time.Time{}
				for _, l := range links {
					if l.ClaimID == claimID {
						got[l.EventID] = l.CreatedAt
					}
				}
				if len(got) != 2 {
					t.Fatalf("%s: links of %s = %+v, want two", label, claimID, links)
				}
				if !got["ev-rec-a"].Equal(thursday) {
					t.Errorf("%s: ev-rec-a recorded at %v, want the supplied %v", label, got["ev-rec-a"], thursday)
				}
				if at := got["ev-rec-b"]; at.Before(before) || at.After(after) {
					t.Errorf("%s: ev-rec-b recorded at %v, want the store's clock in [%v, %v]", label, at, before, after)
				}
			}

			byClaim, err := b.conn.Claims.ListEvidenceByClaimIDs(ctx, []string{"c-rec"})
			if err != nil {
				t.Fatalf("list evidence: %v", err)
			}
			check("ListEvidenceByClaimIDs", byClaim, "c-rec")
			all, err := b.conn.Claims.ListAllEvidence(ctx)
			if err != nil {
				t.Fatalf("list all evidence: %v", err)
			}
			check("ListAllEvidence", all, "c-rec")

			if err := b.conn.Claims.RepointEvidence(ctx, "c-rec", "c-rec-winner"); err != nil {
				t.Fatalf("repoint: %v", err)
			}
			moved, err := b.conn.Claims.ListEvidenceByClaimIDs(ctx, []string{"c-rec-winner"})
			if err != nil {
				t.Fatalf("list moved evidence: %v", err)
			}
			check("after RepointEvidence", moved, "c-rec-winner")
		})
	}
}

// ADR 0036 step 3: every backend that applies credit records each change to
// the applied net credit, so the credit a belief held at T is recoverable —
// whichever write made it: ApplyBeliefCredit, or an upsert carrying
// confidence_components (written by the conflict rule as given).
func TestCreditHistory_RecordsEveryAppliedCreditChange(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)

	for _, b := range openBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			writer, writes := b.conn.Claims.(ports.BeliefCreditWriter)
			lister, lists := b.conn.Claims.(ports.CreditHistoryLister)
			if writes != lists {
				t.Fatalf("BeliefCreditWriter=%v but CreditHistoryLister=%v: a backend that applies credit must version it", writes, lists)
			}
			if !writes {
				t.Skipf("%s does not apply credit", b.name)
			}
			if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{
				ID: "c-credit", Text: "the deploy caused the outage", Type: domain.ClaimTypeFact,
				Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: at,
			}}); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			apply := func(credit float64, extra map[string]float64) {
				t.Helper()
				comps := map[string]float64{domain.CreditAppliedComponentKey: credit}
				for k, v := range extra {
					comps[k] = v
				}
				if err := writer.ApplyBeliefCredit(ctx, "c-credit", comps, 0.8); err != nil {
					t.Fatalf("apply credit %v: %v", credit, err)
				}
			}
			apply(0.1, nil)
			apply(0.1, map[string]float64{domain.InhibitionComponentKey: 0.2}) // credit unchanged: no row
			apply(-0.05, nil)
			upsert := func(components map[string]float64) {
				t.Helper()
				if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{
					ID: "c-credit", Text: "the deploy caused the outage", Type: domain.ClaimTypeFact,
					Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: at,
					ConfidenceComponents: components,
				}}); err != nil {
					t.Fatalf("upsert with components %v: %v", components, err)
				}
			}
			upsert(map[string]float64{domain.CreditAppliedComponentKey: 0.3})  // -0.05 -> 0.3
			upsert(map[string]float64{domain.CreditAppliedComponentKey: 0.3})  // unchanged: no row
			upsert(nil)                                                        // carries no credit: keeps 0.3, no row
			upsert(map[string]float64{domain.SalienceComponentKey: 0.7})       // salience only: keeps 0.3, no row
			upsert(map[string]float64{domain.CreditAppliedComponentKey: -0.2}) // explicit credit wins: 0.3 -> -0.2

			history, err := lister.ListCreditHistory(ctx, []string{"c-credit", "c-missing"})
			if err != nil {
				t.Fatalf("list credit history: %v", err)
			}
			want := []struct{ from, to float64 }{{0, 0.1}, {0.1, -0.05}, {-0.05, 0.3}, {0.3, -0.2}}
			if len(history) != len(want) {
				t.Fatalf("credit history = %+v, want %d changes", history, len(want))
			}
			for i, w := range want {
				h := history[i]
				if h.ClaimID != "c-credit" || h.From != w.from || h.To != w.to || h.ChangedAt.IsZero() {
					t.Errorf("change %d = %+v, want c-credit %v -> %v with a time", i, h, w.from, w.to)
				}
			}
			for i := 1; i < len(history); i++ {
				if history[i].ChangedAt.Before(history[i-1].ChangedAt) {
					t.Errorf("credit history is not oldest first: %+v", history)
				}
			}

			if got, err := b.conn.Claims.ListByIDs(ctx, []string{"c-credit"}); err != nil || len(got) != 1 ||
				domain.AppliedCredit(got[0].ConfidenceComponents) != -0.2 {
				t.Errorf("stored credit after the upserts = %+v (%v), want the explicit -0.2", got, err)
			}

			if err := b.conn.Claims.DeleteCascade(ctx, "c-credit"); err != nil {
				t.Fatalf("delete cascade: %v", err)
			}
			if left, err := lister.ListCreditHistory(ctx, []string{"c-credit"}); err != nil || len(left) != 0 {
				t.Errorf("credit history after DeleteCascade = %+v (%v), want none", left, err)
			}
		})
	}
}

// The conflict rule for confidence_components (domain.UpsertComponents), on
// every backend, from what the store RETURNS: a re-ingest that carries no
// credit must not erase credit a validated outcome applied (the #334
// half_life_days hazard, for credit). An explicit credit still wins.
func TestUpsert_ACreditlessRewriteKeepsAppliedCredit(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
	for _, b := range openBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			writer, ok := b.conn.Claims.(ports.BeliefCreditWriter)
			if !ok {
				t.Skipf("%s does not apply credit", b.name)
			}
			lister := b.conn.Claims.(ports.CreditHistoryLister)
			claim := domain.Claim{
				ID: "c-keep", Text: "the cache is warmed on boot", Type: domain.ClaimTypeFact,
				Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: at,
			}
			if err := b.conn.Claims.Upsert(ctx, []domain.Claim{claim}); err != nil {
				t.Fatalf("insert: %v", err)
			}
			if err := writer.ApplyBeliefCredit(ctx, "c-keep",
				map[string]float64{domain.CreditAppliedComponentKey: 0.15, domain.SalienceComponentKey: 0.8}, 0.8); err != nil {
				t.Fatalf("apply credit: %v", err)
			}
			read := func() map[string]float64 {
				t.Helper()
				got, err := b.conn.Claims.ListByIDs(ctx, []string{"c-keep"})
				if err != nil || len(got) != 1 {
					t.Fatalf("read back: %v (%d)", err, len(got))
				}
				return got[0].ConfidenceComponents
			}
			history := func() int {
				t.Helper()
				h, err := lister.ListCreditHistory(ctx, []string{"c-keep"})
				if err != nil {
					t.Fatalf("credit history: %v", err)
				}
				return len(h)
			}
			rows := history()

			// Write 1: a re-ingest carrying no components at all.
			if err := b.conn.Claims.Upsert(ctx, []domain.Claim{claim}); err != nil {
				t.Fatalf("credit-less re-upsert: %v", err)
			}
			if got := read(); domain.AppliedCredit(got) != 0.15 || got[domain.SalienceComponentKey] != 0.8 {
				t.Errorf("after a credit-less re-upsert components = %v, want the stored credit 0.15 and salience 0.8", got)
			}
			if n := history(); n != rows {
				t.Errorf("a credit-less re-upsert recorded %d credit change(s), want none", n-rows)
			}

			// Write 2: an explicit new credit wins and is recorded.
			claim.ConfidenceComponents = map[string]float64{domain.CreditAppliedComponentKey: 0.4}
			if err := b.conn.Claims.Upsert(ctx, []domain.Claim{claim}); err != nil {
				t.Fatalf("credited re-upsert: %v", err)
			}
			if got := read(); domain.AppliedCredit(got) != 0.4 {
				t.Errorf("after an explicit credit components = %v, want credit 0.4", got)
			}
			if n := history(); n != rows+1 {
				t.Errorf("an explicit credit change recorded %d row(s), want 1", n-rows)
			}
		})
	}
}
