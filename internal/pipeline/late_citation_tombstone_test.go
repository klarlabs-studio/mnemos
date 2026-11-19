package pipeline_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/provenance"
)

// ADR 0038, editing dependencies: a dependency resolved late and then removed
// by hand stays removed. Writing the source again, a second episode carrying
// the same key, or the citing document again all run detection, and none of
// them brings the pair back: Append ignores a stored pair, tombstones
// included. The tombstone is per pair: re-detecting the citing document
// resolves its citation to the mirror too, which is a different pair and is
// recorded.
func TestRecordEventDependencies_DoesNotResurrectARemovedLateDependency(t *testing.T) {
	ctx := context.Background()
	for name, conn := range openLateStores(t) {
		t.Run(name, func(t *testing.T) {
			citing := lateEvent("t_url", "security-blog", "blogger", "Upgrade libfoo.\nDetails at "+advisoryURL+".", nil)
			if err := pipeline.PersistArtifacts(ctx, conn, []domain.Event{citing}, nil, nil, nil); err != nil {
				t.Fatalf("persist citing document: %v", err)
			}
			source := lateEvent("t_adv", "vendor-advisory", "vendor", "libfoo 2.3 fixed the overflow.", map[string]string{provenance.SourcePathKey: advisoryURL})
			if err := pipeline.PersistArtifacts(ctx, conn, []domain.Event{source}, nil, nil, nil); err != nil {
				t.Fatalf("persist source: %v", err)
			}
			live, err := conn.Dependencies.ListByEventIDs(ctx, []string{"t_url"})
			if err != nil || len(live) != 1 || live[0].DependsOn != "t_adv" {
				t.Fatalf("late dependency = %+v (%v), want t_url -> t_adv", live, err)
			}
			if _, err := conn.Dependencies.Remove(ctx, "t_url", "t_adv", "reviewer", time.Now().UTC()); err != nil {
				t.Fatalf("remove: %v", err)
			}

			mirror := lateEvent("t_adv_mirror", "advisory-mirror", "vendor", "libfoo 2.3 fixed the overflow.", map[string]string{provenance.SourcePathKey: advisoryURL})
			for _, again := range [][]domain.Event{{source}, {mirror}, {citing}} {
				if err := pipeline.PersistArtifacts(ctx, conn, again, nil, nil, nil); err != nil {
					t.Fatalf("persist %s again: %v", again[0].ID, err)
				}
				live, err := conn.Dependencies.ListByEventIDs(ctx, []string{"t_url"})
				if err != nil {
					t.Fatal(err)
				}
				for _, d := range live {
					if d.DependsOn == "t_adv" {
						t.Fatalf("after writing %s again the removed dependency t_url -> t_adv is live: %+v", again[0].ID, d)
					}
				}
				if again[0].ID == "t_url" && (len(live) != 1 || live[0].DependsOn != "t_adv_mirror") {
					t.Errorf("after re-detecting the citing document its live dependencies = %+v, want only the mirror", live)
				}
			}
			all, err := conn.Dependencies.ListByEventIDsWithRemoved(ctx, []string{"t_url"})
			if err != nil {
				t.Fatal(err)
			}
			tombstones := 0
			for _, d := range all {
				if d.DependsOn == "t_adv" && d.RemovedAt != nil && d.RemovedBy == "reviewer" {
					tombstones++
				}
			}
			if tombstones != 1 {
				t.Errorf("stored rows of t_url = %+v, want the tombstone on t_adv kept", all)
			}
		})
	}
}
