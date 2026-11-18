package pipeline_test

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/provenance"
	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/trust"
)

const advisoryURL = "https://advisories.example/libfoo-2.3"

func lateEvent(id, source, author, content string, meta map[string]string) domain.Event {
	now := time.Now().UTC()
	return domain.Event{ID: id, RunID: "r", SchemaVersion: "1.0", Content: content, SourceInputID: source,
		Timestamp: now, IngestedAt: now, CreatedBy: author, Metadata: meta}
}

func lateClaim(id, author string) domain.Claim {
	now := time.Now().UTC()
	return domain.Claim{ID: id, Text: "belief " + id, Type: domain.ClaimTypeFact, Confidence: 0.8,
		Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now, CreatedBy: author}
}

func openLateStores(t *testing.T) map[string]*store.Conn {
	t.Helper()
	out := map[string]*store.Conn{}
	for name, dsn := range map[string]string{
		"memory": "memory://late-citations-" + t.Name(),
		"sqlite": "sqlite://" + filepath.Join(t.TempDir(), "late.db"),
	} {
		conn, err := store.Open(context.Background(), dsn)
		if err != nil {
			t.Fatalf("open %s: %v", dsn, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		out[name] = conn
	}
	return out
}

func trustScores(t *testing.T, conn *store.Conn, ids ...string) map[string]float64 {
	t.Helper()
	claims, err := conn.Claims.ListByIDs(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, c := range claims {
		out[c.ID] = c.TrustScore
	}
	return out
}

// ADR 0038, late citations: three documents cite a source that is not stored
// yet, by provenance metadata, by URL and by a named "Source:" line. Nothing
// resolves when they are written. When the source arrives, every citation
// resolves, the pending rows it matched are gone, the citing documents'
// beliefs are rescored as dependent, and a retraction of the source reaches
// them. Asserted on what the store returns, on SQLite and memory.
func TestRecordEventDependencies_ResolvesCitationsOfSourcesStoredLater(t *testing.T) {
	ctx := context.Background()
	for name, conn := range openLateStores(t) {
		t.Run(name, func(t *testing.T) {
			// The citing documents and an independent control, before the source.
			// The newsletter's two chunks are one ingestion of one document, so
			// they share their ingestion time, as parser.Normalizer stamps it.
			named1 := lateEvent("d_named1", "newsletter", "writer", "libfoo 2.3 fixed the overflow.", nil)
			named2 := lateEvent("d_named2", "newsletter", "writer", "Source: the libfoo vendor advisory", nil)
			named2.IngestedAt = named1.IngestedAt
			// Another episode of the same source from a different write: the
			// newsletter's citation does not reach it.
			other := lateEvent("d_other", "newsletter", "writer", "A later issue.", nil)
			other.IngestedAt = named1.IngestedAt.Add(time.Minute)
			if err := pipeline.PersistArtifacts(ctx, conn, []domain.Event{other}, nil, nil, nil); err != nil {
				t.Fatalf("persist the other issue: %v", err)
			}
			citing := []domain.Event{
				lateEvent("d_meta", "digest", "editor", "Upgrade libfoo.", map[string]string{"cites": "adv"}),
				lateEvent("d_url", "security-blog", "blogger", "Upgrade libfoo.\nDetails at "+advisoryURL+".", nil),
				named1, named2,
				lateEvent("e_notes", "release-notes", "maintainer", "libfoo 2.3 was released in March.", nil),
			}
			claims := []domain.Claim{lateClaim("cl_meta", "editor"), lateClaim("cl_url", "blogger"), lateClaim("cl_named", "writer"), lateClaim("cl_ctrl", "maintainer")}
			links := []domain.ClaimEvidence{
				{ClaimID: "cl_meta", EventID: "d_meta"}, {ClaimID: "cl_url", EventID: "d_url"},
				{ClaimID: "cl_named", EventID: "d_named1"}, {ClaimID: "cl_ctrl", EventID: "e_notes"},
			}
			if err := pipeline.PersistArtifacts(ctx, conn, citing, claims, links, nil); err != nil {
				t.Fatalf("persist citing documents: %v", err)
			}
			if deps, err := conn.Dependencies.ListByEventIDs(ctx, []string{"d_meta", "d_url", "d_named1", "d_named2"}); err != nil || len(deps) != 0 {
				t.Fatalf("before the source exists the documents depend on %v (%v), want nothing", deps, err)
			}
			source := lateEvent("adv", "vendor-advisory", "vendor", "libfoo 2.3 fixed the overflow.", map[string]string{provenance.SourcePathKey: advisoryURL})
			sourceKeys := provenance.EpisodeKeys(source)
			pending, err := conn.Dependencies.ListPendingByKeys(ctx, sourceKeys)
			if err != nil {
				t.Fatal(err)
			}
			if got := pendingSummary(pending); got != "[d_meta/adv d_named2/vendor advisory d_url/https: advisories example libfoo 2 3]" {
				t.Fatalf("pending under the source's keys = %s", got)
			}

			// The source arrives and corroborates every belief.
			if err := pipeline.PersistArtifacts(ctx, conn, []domain.Event{source}, nil, []domain.ClaimEvidence{
				{ClaimID: "cl_meta", EventID: "adv"}, {ClaimID: "cl_url", EventID: "adv"},
				{ClaimID: "cl_named", EventID: "adv"}, {ClaimID: "cl_ctrl", EventID: "adv"},
			}, nil); err != nil {
				t.Fatalf("persist source: %v", err)
			}

			deps, err := conn.Dependencies.ListDependents(ctx, []string{"adv"})
			if err != nil {
				t.Fatal(err)
			}
			if got := depSummary(deps); got != "[d_meta cites/metadata d_named1 cites/named_source d_named2 cites/named_source d_url cites/link]" {
				t.Errorf("dependents of the late source = %s", got)
			}
			if left, err := conn.Dependencies.ListPendingByKeys(ctx, sourceKeys); err != nil || len(left) != 0 {
				t.Errorf("pending rows the source matched = %s (%v), want none", pendingSummary(left), err)
			}

			// A dependent document adds no distinct source: each citing belief
			// now weighs less than the control, corroborated by the same source
			// from an independent document.
			scores := trustScores(t, conn, "cl_meta", "cl_url", "cl_named", "cl_ctrl")
			for _, id := range []string{"cl_meta", "cl_url", "cl_named"} {
				if scores[id] > scores["cl_ctrl"]-0.05 {
					t.Errorf("trust %s = %.4f, want clearly below the independent control %.4f", id, scores[id], scores["cl_ctrl"])
				}
			}

			// Retracting the source reaches the dependent documents.
			rep, err := govwrite.ApplyRetraction(ctx, conn, "adv", domain.ChangeNote{Reason: "advisory withdrawn", ChangedBy: "tester"})
			if err != nil {
				t.Fatalf("retract the source: %v", err)
			}
			retracted := append([]string(nil), rep.Impact.RetractedBeliefs...)
			sort.Strings(retracted)
			if fmt.Sprint(retracted) != "[cl_meta cl_named cl_url]" {
				t.Errorf("retracted beliefs = %v, want the three citing documents' beliefs, not the control", retracted)
			}
			var dependents []string
			for _, h := range rep.Impact.DependentEpisodes {
				dependents = append(dependents, h.EventID)
			}
			sort.Strings(dependents)
			if fmt.Sprint(dependents) != "[d_meta d_named1 d_named2 d_url]" {
				t.Errorf("dependent episodes = %v", dependents)
			}
		})
	}
}

// The late dependency rescores the citing document's beliefs itself, so a
// write path that attached the source's evidence before the dependency was
// recorded does not leave trust counting the copy as a second voice.
func TestRecordEventDependencies_RescoresBeliefsOfLateCitingEpisodes(t *testing.T) {
	ctx := context.Background()
	for name, conn := range openLateStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := pipeline.PersistArtifacts(ctx, conn,
				[]domain.Event{lateEvent("d_meta", "digest", "editor", "Upgrade libfoo.", map[string]string{"cites": "adv"})},
				[]domain.Claim{lateClaim("cl", "editor")}, []domain.ClaimEvidence{{ClaimID: "cl", EventID: "d_meta"}}, nil); err != nil {
				t.Fatal(err)
			}
			// The source and its evidence land without the dependency write
			// path, and trust is scored on two independent voices.
			source := lateEvent("adv", "vendor-advisory", "vendor", "libfoo 2.3 fixed the overflow.", nil)
			if err := conn.Events.Append(ctx, source); err != nil {
				t.Fatal(err)
			}
			if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: "cl", EventID: "adv"}}); err != nil {
				t.Fatal(err)
			}
			scorer, ok := conn.Claims.(ports.ScopedTrustScorer)
			if !ok {
				t.Fatalf("%s claims repository is not a ScopedTrustScorer", name)
			}
			if _, err := scorer.RecomputeTrustForClaims(ctx, []string{"cl"}, trust.Scorer(time.Now().UTC())); err != nil {
				t.Fatal(err)
			}
			independent := trustScores(t, conn, "cl")["cl"]

			if err := pipeline.RecordEventDependencies(ctx, conn, []domain.Event{source}); err != nil {
				t.Fatalf("record dependencies: %v", err)
			}
			dependent := trustScores(t, conn, "cl")["cl"]
			if dependent > independent-0.05 {
				t.Errorf("trust after the late dependency = %.4f, want clearly below the independent %.4f", dependent, independent)
			}
			if _, err := scorer.RecomputeTrustForClaims(ctx, []string{"cl"}, trust.Scorer(time.Now().UTC())); err != nil {
				t.Fatal(err)
			}
			if fresh := trustScores(t, conn, "cl")["cl"]; math.Abs(fresh-dependent) > 1e-6 {
				t.Errorf("stored trust %.6f is stale: a fresh recompute gives %.6f", dependent, fresh)
			}
		})
	}
}

// One write re-detects at most MaxLateCitingDocuments citing documents, oldest
// citation first; the rest keep their pending rows and resolve when another
// episode carrying the key is written.
func TestRecordEventDependencies_BoundsLateCitingDocumentsPerWrite(t *testing.T) {
	ctx := context.Background()
	conn, err := store.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "bound.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	n := pipeline.MaxLateCitingDocuments + 3
	for i := 0; i < n; i++ {
		ev := lateEvent(fmt.Sprintf("d%02d", i), fmt.Sprintf("doc-%02d", i), "editor", "Upgrade.", map[string]string{"cites": "vendor-advisory"})
		if err := pipeline.PersistArtifacts(ctx, conn, []domain.Event{ev}, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	first := lateEvent("adv1", "vendor-advisory", "vendor", "Part one.", nil)
	if err := pipeline.PersistArtifacts(ctx, conn, []domain.Event{first}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	deps, err := conn.Dependencies.ListDependents(ctx, []string{"adv1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != pipeline.MaxLateCitingDocuments {
		t.Errorf("first write resolved %d citing documents, want the bound %d", len(deps), pipeline.MaxLateCitingDocuments)
	}
	left, err := conn.Dependencies.ListPendingByKeys(ctx, []string{"vendor advisory"})
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("[d%02d/vendor advisory d%02d/vendor advisory d%02d/vendor advisory]", n-3, n-2, n-1)
	if got := pendingSummary(left); got != want {
		t.Errorf("still pending after the first write = %s, want the three newest citations %s", got, want)
	}
	// The next episode of the source resolves the rest, against every stored
	// episode of the source.
	second := lateEvent("adv2", "vendor-advisory", "vendor", "Part two.", nil)
	if err := pipeline.PersistArtifacts(ctx, conn, []domain.Event{second}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	last := fmt.Sprintf("d%02d", n-1)
	out, err := conn.Dependencies.ListByEventIDs(ctx, []string{last})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Errorf("%s depends on %s, want both episodes of the source", last, depSummary(out))
	}
	if left, err := conn.Dependencies.ListPendingByKeys(ctx, []string{"vendor advisory"}); err != nil || len(left) != 0 {
		t.Errorf("still pending after the second write = %s (%v), want none", pendingSummary(left), err)
	}
}

func pendingSummary(refs []domain.PendingReference) string {
	out := make([]string, len(refs))
	for i, p := range refs {
		out[i] = p.EventID + "/" + p.LookupKey
	}
	sort.Strings(out)
	return fmt.Sprint(out)
}

func depSummary(deps []domain.EventDependency) string {
	out := make([]string, len(deps))
	for i, d := range deps {
		out[i] = fmt.Sprintf("%s %s/%s", d.EventID, d.Kind, d.Signal)
	}
	sort.Strings(out)
	return fmt.Sprint(out)
}
