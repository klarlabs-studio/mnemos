package main

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

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

// End to end through the hook worker and the capture write path (ADR 0048):
// a session the recall hook showed a belief, which then says it back, adds an
// evidence episode (provenance) but no independent source, so the belief's
// trust does not move. A session that was never shown it is a second source.
func TestE2E_ASessionRecallSeededDoesNotCorroborate(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "exposure.db"))
	text := "The retry budget for the outbox drain is three attempts."
	if _, err := mcpRunProcessText(ctx, captureActor, mcpProcessTextInput{Text: text, SessionID: "sess-origin"}); err != nil {
		t.Fatal(err)
	}
	claimID, inputs := onlyClaimInputs(t)
	before := inputs.EvidenceCount

	recordRecallExposures(ctx, recallExposurePayload{
		SessionID: "sess-recalled", ShownAt: time.Now().UTC().Add(-time.Second),
		Shown: map[string][]string{"global": {claimID}},
	})
	if _, err := mcpRunProcessText(ctx, captureActor, mcpProcessTextInput{Text: text, SessionID: "sess-recalled"}); err != nil {
		t.Fatal(err)
	}
	if got := countBrain(t); got.claims != 1 || got.evidence != 2 {
		t.Fatalf("brain = %+v, want one belief with both episodes kept as provenance", got)
	}
	if _, in := onlyClaimInputs(t); in.EvidenceCount != before {
		t.Errorf("a recall-seeded restatement moved the evidence count %d -> %d", before, in.EvidenceCount)
	}

	if _, err := mcpRunProcessText(ctx, captureActor, mcpProcessTextInput{Text: text, SessionID: "sess-independent"}); err != nil {
		t.Fatal(err)
	}
	if _, in := onlyClaimInputs(t); in.EvidenceCount != before+1 {
		t.Errorf("an independent session left the evidence count at %d, want %d", in.EvidenceCount, before+1)
	}
}

func onlyClaimInputs(t *testing.T) (string, struct{ EvidenceCount int }) {
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
	inputs, err := conn.Claims.(ports.TrustInputLister).ListTrustInputs(ctx, []string{claims[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	return claims[0].ID, struct{ EvidenceCount int }{inputs[claims[0].ID].EvidenceCount}
}
