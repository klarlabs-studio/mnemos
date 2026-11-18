package mnemos_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
)

// ADR 0038: an event whose metadata says it copies another is not independent
// evidence. Retracting the original withdraws the claim the copy kept alive,
// and the library report names the dependent event and the path.
func TestRetractEvent_ReportsDependentEvents(t *testing.T) {
	mem := newRetractMemory(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "ev_src", At: now, Type: "observation", Content: "the original"}); err != nil {
		t.Fatal(err)
	}
	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "ev_copy", At: now, Type: "observation", Content: "a repost",
		Metadata: map[string]string{"copied_from": "ev_src"}}); err != nil {
		t.Fatal(err)
	}
	copied, err := mem.RememberClaim(ctx, mnemos.ClaimItem{Text: "The cache is warm after deploy.", EventIDs: []string{"ev_src", "ev_copy"}, ValidFrom: now})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := mem.RetractEvent(ctx, "ev_src", "wrong")
	if err != nil {
		t.Fatalf("RetractEvent: %v", err)
	}
	if got := rep.Impact.RetractedClaims; len(got) != 1 || got[0] != copied {
		t.Errorf("retracted claims = %v, want [%s]", got, copied)
	}
	if d := rep.Impact.DependentEvents; len(d) != 1 || d[0].EventID != "ev_copy" || d[0].DependsOn != "ev_src" || d[0].Kind != "copies" || d[0].Signal != "metadata" {
		t.Errorf("dependent events = %+v", d)
	}
	if p := rep.Impact.ClaimPaths; len(p) != 1 || p[0].ClaimID != copied || p[0].Outcome != "retracted" {
		t.Errorf("claim paths = %+v", p)
	}
}
