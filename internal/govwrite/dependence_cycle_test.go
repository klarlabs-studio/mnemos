package govwrite_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// A declared dependency can close a cycle (ADR 0038): ev_a depends on ev_b
// and on the source ev_r, and ev_b depends back on ev_a. Retracting ev_r
// must reach a belief resting only on ev_b. This is the end-to-end view;
// TestIndependence_CycleIsOrderIndependent pins the evaluation orders, since
// which one this path takes depends on how the store returns rows.
func TestRetractEvent_ReachesThroughADependencyCycle(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		first, last string // belief ids; links are read in id order
	}{
		{name: "cycle entered at ev_a", first: "cl_1", last: "cl_2"},
		{name: "cycle entered at ev_b", first: "cl_2", last: "cl_1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWriter(t)
			ctx := context.Background()
			now := time.Now().UTC()
			var events []domain.Event
			for _, id := range []string{"ev_r", "ev_a", "ev_b"} {
				events = append(events, domain.Event{ID: id, RunID: "r1", Content: "content of " + id, SchemaVersion: "1.0",
					SourceInputID: "src-" + id, Timestamp: now, IngestedAt: now, CreatedBy: "tester"})
			}
			if _, err := w.Events(ctx, events); err != nil {
				t.Fatalf("seed events: %v", err)
			}
			for _, d := range []domain.EventDependency{
				{EventID: "ev_a", DependsOn: "ev_r", Kind: domain.DependencyCopies},
				{EventID: "ev_a", DependsOn: "ev_b", Kind: domain.DependencyCites},
				{EventID: "ev_b", DependsOn: "ev_a", Kind: domain.DependencyCites},
			} {
				d.CreatedBy = "tester"
				if _, err := w.DeclareEventDependency(ctx, d); err != nil {
					t.Fatalf("declare %s -> %s: %v", d.EventID, d.DependsOn, err)
				}
			}
			onA, onB := tc.first, tc.last
			seedClaim(t, w, onA)
			seedClaim(t, w, onB)
			if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{
				{ClaimID: onA, EventID: "ev_a"}, {ClaimID: onB, EventID: "ev_b"},
			}); err != nil {
				t.Fatalf("seed links: %v", err)
			}

			if _, err := w.RetractEvent(ctx, "ev_r", domain.ChangeNote{Reason: "source withdrawn", ChangedBy: "tester"}); err != nil {
				t.Fatalf("RetractEvent: %v", err)
			}
			got := statuses(t, w, onA, onB)
			for _, id := range []string{onA, onB} {
				if got[id] != domain.ClaimStatusDeprecated {
					t.Errorf("%s = %s, want deprecated: its only evidence depends on the retracted ev_r", id, got[id])
				}
			}
		})
	}
}

// The same cycle with nothing retracted downstream of it: a cycle alone
// taints nothing, so beliefs on it are kept.
func TestRetractEvent_CycleWithoutRetractedSourceKeepsBeliefs(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var events []domain.Event
	for _, id := range []string{"ev_r", "ev_a", "ev_b", "ev_c"} {
		events = append(events, domain.Event{ID: id, RunID: "r1", Content: "content of " + id, SchemaVersion: "1.0",
			SourceInputID: "src-" + id, Timestamp: now, IngestedAt: now, CreatedBy: "tester"})
	}
	if _, err := w.Events(ctx, events); err != nil {
		t.Fatalf("seed events: %v", err)
	}
	for _, d := range []domain.EventDependency{
		{EventID: "ev_a", DependsOn: "ev_b", Kind: domain.DependencyCites},
		{EventID: "ev_b", DependsOn: "ev_a", Kind: domain.DependencyCites},
	} {
		d.CreatedBy = "tester"
		if _, err := w.DeclareEventDependency(ctx, d); err != nil {
			t.Fatalf("declare: %v", err)
		}
	}
	seedClaim(t, w, "cl_ab")
	seedClaim(t, w, "cl_r")
	// cl_ab rests on the cycle and on ev_r; retracting ev_r leaves the
	// cycle's evidence, which depends on nothing retracted.
	if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl_ab", EventID: "ev_r"}, {ClaimID: "cl_ab", EventID: "ev_a"}, {ClaimID: "cl_ab", EventID: "ev_b"},
		{ClaimID: "cl_r", EventID: "ev_r"},
	}); err != nil {
		t.Fatalf("seed links: %v", err)
	}
	if _, err := w.RetractEvent(ctx, "ev_r", domain.ChangeNote{Reason: "source withdrawn", ChangedBy: "tester"}); err != nil {
		t.Fatalf("RetractEvent: %v", err)
	}
	got := statuses(t, w, "cl_ab", "cl_r")
	if got["cl_ab"] == domain.ClaimStatusDeprecated {
		t.Errorf("cl_ab deprecated; its cycle evidence depends on nothing retracted")
	}
	if got["cl_r"] != domain.ClaimStatusDeprecated {
		t.Errorf("cl_r = %s, want deprecated", got["cl_r"])
	}
}
