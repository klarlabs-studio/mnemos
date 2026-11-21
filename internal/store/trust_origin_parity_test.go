package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/recorded"
	"go.klarlabs.de/mnemos/internal/trust"
)

// originEpisode is one evidence episode of a scenario: who ingested it, from
// which input, with what metadata and text.
type originEpisode struct {
	createdBy, sourceInput, content string
	meta                            map[string]string
}

// ADR 0046: a belief's distinct sources are the distinct ORIGINS of its
// evidence (domain.EvidenceOrigin) — the file it came from, else the session,
// else its normalised text — never the principal that ingested it and never
// the input id. Every backend hands the scorer the same inputs, the stored
// trust is trust.At of them, and the recorded-as-of reconstruction agrees.
func TestTrustInput_EvidenceIndependenceIsKeyedOnOriginAcrossBackends(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	path := func(p string) map[string]string { return map[string]string{"input_source_path": p} }
	session := func(s string) map[string]string { return map[string]string{"session_id": s} }

	scenarios := []struct {
		name           string
		episodes       []originEpisode
		dependsOnFirst bool // the second episode copies the first (ADR 0038)
		wantDistinct   int
	}{
		{
			name: "same file ingested by two principals is one source",
			episodes: []originEpisode{
				{createdBy: "alice", sourceInput: "in-a1", content: "chunk one", meta: path("/docs/runbook.md")},
				{createdBy: "bob", sourceInput: "in-a2", content: "chunk two", meta: path("/docs/runbook.md")},
			},
			wantDistinct: 1,
		},
		{
			name: "two different files by one principal are two sources",
			episodes: []originEpisode{
				{createdBy: "alice", sourceInput: "in-b1", content: "the same sentence", meta: path("/docs/a.md")},
				{createdBy: "alice", sourceInput: "in-b2", content: "the same sentence", meta: path("/docs/b.md")},
			},
			wantDistinct: 2,
		},
		{
			name: "two sessions are two sources",
			episodes: []originEpisode{
				{createdBy: "alice", sourceInput: "in-c1", content: "said in one", meta: session("sess-1")},
				{createdBy: "alice", sourceInput: "in-c2", content: "said in two", meta: session("sess-2")},
			},
			wantDistinct: 2,
		},
		{
			name: "one session restating a belief in many messages is one source",
			episodes: []originEpisode{
				{createdBy: "alice", sourceInput: "in-d1", content: "first message", meta: session("sess-many")},
				{createdBy: "bob", sourceInput: "in-d2", content: "second message", meta: session("sess-many")},
				{createdBy: "carol", sourceInput: "in-d3", content: "third message", meta: session("sess-many")},
				{createdBy: "dave", sourceInput: "in-d4", content: "fourth message", meta: map[string]string{"input_session_id": "sess-many"}},
			},
			wantDistinct: 1,
		},
		{
			name: "a file path outranks the session it was read in",
			episodes: []originEpisode{
				{createdBy: "alice", sourceInput: "in-e1", content: "part one", meta: map[string]string{"input_source_path": "/docs/c.md", "session_id": "sess-x"}},
				{createdBy: "alice", sourceInput: "in-e2", content: "part two", meta: map[string]string{"input_source_path": "/docs/c.md", "session_id": "sess-y"}},
			},
			wantDistinct: 1,
		},
		{
			name: "identical text across two inputs without path or session is one source",
			episodes: []originEpisode{
				{createdBy: "alice", sourceInput: "in-f1", content: "Deploys freeze on Fridays."},
				{createdBy: "bob", sourceInput: "in-f2", content: "  deploys freeze on fridays.  "},
			},
			wantDistinct: 1,
		},
		{
			name: "distinct texts without path or session are two sources",
			episodes: []originEpisode{
				{createdBy: "alice", sourceInput: "in-g1", content: "deploys freeze on fridays"},
				{createdBy: "alice", sourceInput: "in-g2", content: "no deploys on friday afternoons"},
			},
			wantDistinct: 2,
		},
		{
			name: "a dependent episode still adds no source (ADR 0038)",
			episodes: []originEpisode{
				{createdBy: "alice", sourceInput: "in-h1", content: "the advisory", meta: path("/docs/advisory.md")},
				{createdBy: "alice", sourceInput: "in-h2", content: "the blog", meta: path("/docs/blog.md")},
			},
			dependsOnFirst: true,
			wantDistinct:   1,
		},
	}

	for _, b := range openBackends(t) {
		claimIDs := make([]string, 0, len(scenarios))
		want := map[string]int{}
		for i, sc := range scenarios {
			claimID := fmt.Sprintf("to-claim-%d", i)
			claimIDs = append(claimIDs, claimID)
			want[claimID] = domain.EffectiveEvidenceCount(sc.wantDistinct, len(sc.episodes))
			if err := b.conn.Claims.Upsert(ctx, []domain.Claim{{
				ID: claimID, Text: sc.name, Type: domain.ClaimTypeFact, Confidence: 0.7,
				Status: domain.ClaimStatusActive, CreatedAt: at.Add(-48 * time.Hour),
			}}); err != nil {
				t.Fatalf("%s: upsert %s: %v", b.name, claimID, err)
			}
			var links []domain.ClaimEvidence
			for j, ep := range sc.episodes {
				ev := domain.Event{
					ID: fmt.Sprintf("to-ev-%d-%d", i, j), RunID: "run-origin", SchemaVersion: "v1",
					Content: ep.content, SourceInputID: ep.sourceInput, Metadata: ep.meta, CreatedBy: ep.createdBy,
					Timestamp: at.Add(-48 * time.Hour), IngestedAt: at.Add(-48 * time.Hour),
				}
				if err := b.conn.Events.Append(ctx, ev); err != nil {
					t.Fatalf("%s: append %s: %v", b.name, ev.ID, err)
				}
				links = append(links, domain.ClaimEvidence{ClaimID: claimID, EventID: ev.ID, CreatedAt: at.Add(-48 * time.Hour)})
			}
			if err := b.conn.Claims.UpsertEvidence(ctx, links); err != nil {
				t.Fatalf("%s: evidence %s: %v", b.name, claimID, err)
			}
			if sc.dependsOnFirst {
				if err := b.conn.Dependencies.Append(ctx, []domain.EventDependency{{
					EventID: links[1].EventID, DependsOn: links[0].EventID, Kind: domain.DependencyCopies,
					Signal: domain.SignalMetadata, Reference: "copied_from: " + links[0].EventID,
					CreatedAt: at.Add(-24 * time.Hour), CreatedBy: "tester",
				}}); err != nil {
					t.Fatalf("%s: dependency %s: %v", b.name, claimID, err)
				}
			}
		}

		lister, ok := b.conn.Claims.(ports.TrustInputLister)
		if !ok {
			t.Fatalf("%s: not a TrustInputLister", b.name)
		}
		inputs, err := lister.ListTrustInputs(ctx, claimIDs)
		if err != nil {
			t.Fatalf("%s: trust inputs: %v", b.name, err)
		}
		for i, id := range claimIDs {
			if got := inputs[id].EvidenceCount; got != want[id] {
				t.Errorf("%s: %s: evidence count = %d, want %d", b.name, scenarios[i].name, got, want[id])
			}
		}

		scorer, ok := b.conn.Claims.(ports.ScopedTrustScorer)
		if !ok {
			t.Fatalf("%s: not a ScopedTrustScorer", b.name)
		}
		if _, err := scorer.RecomputeTrustForClaims(ctx, claimIDs, trust.Scorer(at)); err != nil {
			t.Fatalf("%s: rescore: %v", b.name, err)
		}
		stored, err := b.conn.Claims.ListByIDs(ctx, claimIDs)
		if err != nil {
			t.Fatalf("%s: read back: %v", b.name, err)
		}
		view := recorded.Claims{ClaimRepository: b.conn.Claims, Events: b.conn.Events, Dependencies: b.conn.Dependencies, At: at}
		recordedClaims, err := view.ListByIDs(ctx, claimIDs)
		if err != nil {
			t.Fatalf("%s: recorded read: %v", b.name, err)
		}
		recordedTrust := map[string]float64{}
		for _, c := range recordedClaims {
			recordedTrust[c.ID] = c.TrustScore
		}
		for _, c := range stored {
			live := trust.At(inputs[c.ID], at)
			if !closeTo(c.TrustScore, live) {
				t.Errorf("%s: %s: stored trust = %v, want trust.At of its inputs %v", b.name, c.Text, c.TrustScore, live)
			}
			if got, ok := recordedTrust[c.ID]; !ok || !closeTo(got, live) {
				t.Errorf("%s: %s: recorded-as-of trust = %v (present %v), want live trust %v", b.name, c.Text, got, ok, live)
			}
		}
	}
}
