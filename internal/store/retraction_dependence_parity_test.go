package store_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
)

// ADR 0038 §4: on every backend, the governed episode write detects a
// citation, and retracting the cited episode withdraws the belief its copy
// kept alive while the independently corroborated belief survives.
func TestRetractEvent_FollowsDependenceOnEveryBackend(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	for _, b := range openBackends(t) {
		w, err := govwrite.Wrap(b.conn, nil)
		if err != nil {
			t.Fatalf("%s: wrap: %v", b.name, err)
		}
		if _, err := w.Events(ctx, []domain.Event{
			{ID: "ev-dp-adv", RunID: "dp", Content: "libfoo 2.3 fixed the overflow", SchemaVersion: "v1", SourceInputID: "vendor-advisory", Timestamp: now, IngestedAt: now},
			{ID: "ev-dp-notes", RunID: "dp", Content: "libfoo 2.3 shipped in March", SchemaVersion: "v1", SourceInputID: "release-notes", Timestamp: now, IngestedAt: now},
		}); err != nil {
			t.Fatalf("%s: sources: %v", b.name, err)
		}
		if _, err := w.Events(ctx, []domain.Event{
			{ID: "ev-dp-blog", RunID: "dp", Content: "libfoo 2.3 fixed the overflow\nSource: the vendor advisory", SchemaVersion: "v1", SourceInputID: "security-blog", Timestamp: now, IngestedAt: now},
		}); err != nil {
			t.Fatalf("%s: blog: %v", b.name, err)
		}
		if _, err := w.Claims(ctx, []domain.Claim{
			{ID: "cl-dp-false", Text: "libfoo 2.3 fixed the overflow", Type: domain.ClaimTypeFact, Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now},
			{ID: "cl-dp-date", Text: "libfoo 2.3 shipped in March", Type: domain.ClaimTypeFact, Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now},
		}, govwrite.ClaimReason{Reason: "seed"}); err != nil {
			t.Fatalf("%s: claims: %v", b.name, err)
		}
		if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{
			{ClaimID: "cl-dp-false", EventID: "ev-dp-adv"}, {ClaimID: "cl-dp-false", EventID: "ev-dp-blog"},
			{ClaimID: "cl-dp-date", EventID: "ev-dp-adv"}, {ClaimID: "cl-dp-date", EventID: "ev-dp-notes"},
		}); err != nil {
			t.Fatalf("%s: links: %v", b.name, err)
		}

		rep, err := w.RetractEvent(ctx, "ev-dp-adv", domain.ChangeNote{Reason: "withdrawn", ChangedBy: "tester"})
		if err != nil {
			t.Fatalf("%s: retract: %v", b.name, err)
		}
		if len(rep.Impact.DependentEpisodes) != 1 || rep.Impact.DependentEpisodes[0].EventID != "ev-dp-blog" ||
			rep.Impact.DependentEpisodes[0].Signal != domain.SignalNamedSource {
			t.Errorf("%s: dependent episodes = %+v, want the blog by named source", b.name, rep.Impact.DependentEpisodes)
		}
		got, err := b.conn.Claims.ListByIDs(ctx, []string{"cl-dp-false", "cl-dp-date"})
		if err != nil {
			t.Fatalf("%s: read back: %v", b.name, err)
		}
		status := map[string]domain.ClaimStatus{}
		for _, c := range got {
			status[c.ID] = c.Status
		}
		if status["cl-dp-false"] != domain.ClaimStatusDeprecated || status["cl-dp-date"] != domain.ClaimStatusActive {
			t.Errorf("%s: statuses = %v, want the copied claim withdrawn and the corroborated one kept", b.name, status)
		}
	}
}
