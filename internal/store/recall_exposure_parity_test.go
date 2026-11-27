package store_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// ADR 0048: every backend records which beliefs recall showed to which session,
// keeps the FIRST exposure of a (session, belief), rejects a row that could
// never match, and lists the exposures of the beliefs asked for.
func TestRecallExposures_AgreeAcrossBackends(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 1, 9, 30, 0, 123456000, time.UTC)
	for _, b := range openBackends(t) {
		repo, ok := b.conn.Claims.(ports.RecallExposureRepository)
		if !ok {
			t.Fatalf("%s: claim repository is not a RecallExposureRepository", b.name)
		}
		if err := repo.RecordRecallExposures(ctx, []domain.RecallExposure{
			{SessionID: "sess-a", ClaimID: "c1", ExposedAt: at},
			{SessionID: "sess-a", ClaimID: "c2", ExposedAt: at.Add(time.Minute)},
			{SessionID: "sess-b", ClaimID: "c1", ExposedAt: at.Add(2 * time.Minute)},
		}); err != nil {
			t.Fatalf("%s: record: %v", b.name, err)
		}
		// A later exposure of the same (session, belief) keeps the first.
		if err := repo.RecordRecallExposures(ctx, []domain.RecallExposure{
			{SessionID: "sess-a", ClaimID: "c1", ExposedAt: at.Add(time.Hour)},
		}); err != nil {
			t.Fatalf("%s: record again: %v", b.name, err)
		}
		if err := repo.RecordRecallExposures(ctx, []domain.RecallExposure{{ClaimID: "c1", ExposedAt: at}}); err == nil {
			t.Errorf("%s: an exposure without a session was accepted", b.name)
		}

		got, err := repo.ListRecallExposures(ctx, []string{"c1"})
		if err != nil {
			t.Fatalf("%s: list: %v", b.name, err)
		}
		want := []domain.RecallExposure{
			{SessionID: "sess-a", ClaimID: "c1", ExposedAt: at},
			{SessionID: "sess-b", ClaimID: "c1", ExposedAt: at.Add(2 * time.Minute)},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: exposures of c1 = %+v, want %+v", b.name, got, want)
		}
		if none, err := repo.ListRecallExposures(ctx, nil); err != nil || len(none) != 0 {
			t.Errorf("%s: list of no beliefs = %+v, %v; want empty", b.name, none, err)
		}
	}
}
