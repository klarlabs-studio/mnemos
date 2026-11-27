package main

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// Only the beliefs renderRecall shows enter the session's context, so only
// those are exposures, grouped by the tier (brain) each came from.
func TestShownExposures_AreTheRenderedBeliefsByTier(t *testing.T) {
	var claims []recallClaim
	for i := 0; i < maxRecallLines+2; i++ {
		src := "global"
		if i%2 == 1 {
			src = "workspace"
		}
		claims = append(claims, recallClaim{ID: fmt.Sprintf("c%d", i), Source: src})
	}
	claims[2].ID = "" // a hosted claim carries no id: nothing to key on
	got := shownExposures(claims)
	want := map[string][]string{"global": {"c0", "c4"}, "workspace": {"c1", "c3", "c5"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shown = %v, want %v (the first %d rendered lines only)", got, want, maxRecallLines)
	}
}

// End to end through the hook worker (#443): the exposures the recall hook
// hands its worker land in the brain, the first exposure of a (session,
// belief) is the one kept, and the session that then says the belief back is
// classified as recall-seeded while a session never shown it is not. Nothing
// reads the rows into trust yet; this pins the record the measurement needs.
func TestE2E_RecallHookWorkerRecordsExposures(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "exposure.db"))
	text := "The retry budget for the outbox drain is three attempts."
	if _, err := mcpRunProcessText(ctx, captureActor, mcpProcessTextInput{Text: text, SessionID: "sess-origin"}); err != nil {
		t.Fatal(err)
	}
	claimID := onlyClaimID(t)

	first := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	recordRecallExposures(ctx, recallExposurePayload{
		SessionID: "sess-recalled", ShownAt: first,
		Shown: map[string][]string{"global": {claimID}},
	})
	recordRecallExposures(ctx, recallExposurePayload{
		SessionID: "sess-recalled", ShownAt: first.Add(30 * time.Second),
		Shown: map[string][]string{"global": {claimID}},
	})
	for _, s := range []string{"sess-recalled", "sess-independent"} {
		if _, err := mcpRunProcessText(ctx, captureActor, mcpProcessTextInput{Text: text, SessionID: s}); err != nil {
			t.Fatal(err)
		}
	}

	conn, err := openConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	exposures, err := conn.Claims.(ports.RecallExposureRepository).ListRecallExposures(ctx, []string{claimID})
	if err != nil {
		t.Fatal(err)
	}
	if len(exposures) != 1 || exposures[0].SessionID != "sess-recalled" || !exposures[0].ExposedAt.Equal(first) {
		t.Fatalf("exposures = %+v, want the first exposure of sess-recalled only", exposures)
	}

	events, err := conn.Events.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seeded := map[string]bool{}
	for _, ev := range events {
		if s := domain.EvidenceSession(ev); s != "" {
			seeded[s] = domain.RecallSeeded(ev, claimID, exposures)
		}
	}
	want := map[string]bool{"sess-origin": false, "sess-recalled": true, "sess-independent": false}
	if !reflect.DeepEqual(seeded, want) {
		t.Errorf("recall-seeded by session = %v, want %v", seeded, want)
	}
}

func onlyClaimID(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	claims, err := conn.Claims.ListAll(ctx)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims = %d (%v), want exactly one", len(claims), err)
	}
	return claims[0].ID
}
