package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/query"
	"go.klarlabs.de/mnemos/internal/trust"
)

// ADR 0037: retracting an event behaves the same on every backend, and a
// recorded-as-of read from before the retraction (ADR 0036) still returns the
// belief the retraction withdrew. Asserted on what each store returns.
func TestRetractEvent_AgreesAcrossBackends(t *testing.T) {
	ctx := context.Background()
	for _, b := range openBackends(t) {
		w, err := govwrite.Wrap(b.conn, nil)
		if err != nil {
			t.Fatalf("%s: wrap: %v", b.name, err)
		}
		now := time.Now().UTC()
		if _, err := w.Events(ctx, []domain.Event{
			{ID: "ev-rx-keep", RunID: "rx", Content: "the billing service retries failed charges three times", SchemaVersion: "v1", SourceInputID: "src-keep", Timestamp: now, IngestedAt: now},
			{ID: "ev-rx-gone", RunID: "rx", Content: "the billing service retries failed charges three times; billing writes to the ledger nightly", SchemaVersion: "v1", SourceInputID: "src-gone", Timestamp: now, IngestedAt: now},
		}); err != nil {
			t.Fatalf("%s: events: %v", b.name, err)
		}
		if _, err := w.Claims(ctx, []domain.Claim{
			{ID: "cl-rx-shared", Text: "the billing service retries failed charges three times", Type: domain.ClaimTypeFact, Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now.Add(-time.Hour)},
			{ID: "cl-rx-only", Text: "billing writes to the ledger nightly", Type: domain.ClaimTypeFact, Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now.Add(-time.Hour)},
		}, govwrite.ClaimReason{Reason: "seed"}); err != nil {
			t.Fatalf("%s: claims: %v", b.name, err)
		}
		if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{
			{ClaimID: "cl-rx-shared", EventID: "ev-rx-keep"},
			{ClaimID: "cl-rx-shared", EventID: "ev-rx-gone"},
			{ClaimID: "cl-rx-only", EventID: "ev-rx-gone"},
		}); err != nil {
			t.Fatalf("%s: links: %v", b.name, err)
		}
		if scorer, ok := b.conn.Claims.(ports.TrustScorer); ok {
			if _, err := scorer.RecomputeTrust(ctx, trust.Scorer(now)); err != nil {
				t.Fatalf("%s: score: %v", b.name, err)
			}
		}
		time.Sleep(5 * time.Millisecond)
		beforeRetraction := time.Now().UTC()
		time.Sleep(5 * time.Millisecond)

		rep, err := w.RetractEvent(ctx, "ev-rx-gone", domain.ChangeNote{Reason: "source corrected", ChangedBy: "tester"})
		if err != nil {
			t.Fatalf("%s: retract: %v", b.name, err)
		}
		if len(rep.Impact.RetractedBeliefs) != 1 || rep.Impact.RetractedBeliefs[0] != "cl-rx-only" {
			t.Errorf("%s: retracted = %v, want [cl-rx-only]", b.name, rep.Impact.RetractedBeliefs)
		}

		got, err := b.conn.Claims.ListByIDs(ctx, []string{"cl-rx-only", "cl-rx-shared"})
		if err != nil {
			t.Fatalf("%s: read back: %v", b.name, err)
		}
		byID := map[string]domain.Claim{}
		for _, c := range got {
			byID[c.ID] = c
		}
		if byID["cl-rx-only"].Status != domain.ClaimStatusDeprecated {
			t.Errorf("%s: cl-rx-only status = %s, want deprecated", b.name, byID["cl-rx-only"].Status)
		}
		if byID["cl-rx-shared"].Status != domain.ClaimStatusActive {
			t.Errorf("%s: cl-rx-shared status = %s, want active", b.name, byID["cl-rx-shared"].Status)
		}
		history, err := b.conn.Claims.ListStatusHistoryByClaimID(ctx, "cl-rx-only")
		if err != nil || len(history) == 0 {
			t.Fatalf("%s: history: %v (%d rows)", b.name, err, len(history))
		}
		if last := history[len(history)-1]; last.ToStatus != domain.ClaimStatusDeprecated || !strings.HasPrefix(last.Reason, domain.RetractionReasonPrefix) || last.ChangedBy != "tester" {
			t.Errorf("%s: last transition = %+v, want a retraction to deprecated by tester", b.name, last)
		}
		links, err := b.conn.Claims.ListEvidenceByClaimIDs(ctx, []string{"cl-rx-shared"})
		if err != nil || len(links) != 1 || links[0].EventID != "ev-rx-keep" {
			t.Errorf("%s: cl-rx-shared evidence = %+v (%v), want only ev-rx-keep", b.name, links, err)
		}
		if len(rep.Impact.Rescored) != 1 || rep.Impact.Rescored[0].TrustAfter != byID["cl-rx-shared"].TrustScore {
			t.Errorf("%s: rescored = %+v, stored trust %.3f", b.name, rep.Impact.Rescored, byID["cl-rx-shared"].TrustScore)
		}
		if _, ok, err := govwrite.LookupRetraction(ctx, b.conn, "ev-rx-gone"); err != nil || !ok {
			t.Errorf("%s: retraction not recorded: ok=%v err=%v", b.name, ok, err)
		}

		// ADR 0036: as the brain knew it before the retraction, the belief was held.
		engine := query.NewEngine(b.conn.Events, b.conn.Claims, b.conn.Relationships)
		const question = "billing ledger nightly"
		past, err := engine.AnswerWithOptions(ctx, question, query.AnswerOptions{RecordedAsOf: beforeRetraction})
		if err != nil {
			t.Fatalf("%s: as-of answer: %v", b.name, err)
		}
		if !answerHas(past, "cl-rx-only") {
			t.Errorf("%s: a read recorded before the retraction lost cl-rx-only: %v", b.name, answerIDs(past))
		}
		present, err := engine.AnswerWithOptions(ctx, question, query.AnswerOptions{})
		if err != nil {
			t.Fatalf("%s: answer: %v", b.name, err)
		}
		if answerHas(present, "cl-rx-only") {
			t.Errorf("%s: today's read still returns the retracted belief", b.name)
		}

		// Repeating the retraction changes nothing.
		again, err := w.RetractEvent(ctx, "ev-rx-gone", domain.ChangeNote{Reason: "again"})
		if err != nil || !again.AlreadyRetracted {
			t.Errorf("%s: repeat = %+v, %v; want AlreadyRetracted", b.name, again, err)
		}
		historyAgain, _ := b.conn.Claims.ListStatusHistoryByClaimID(ctx, "cl-rx-only")
		if len(historyAgain) != len(history) {
			t.Errorf("%s: repeat retraction grew the history %d → %d", b.name, len(history), len(historyAgain))
		}

		// ADR 0037 §8: un-retraction restores the retracted belief and the
		// rescored belief's link, on every backend.
		un, err := w.UnretractEvent(ctx, "ev-rx-gone", domain.ChangeNote{Reason: "source was right", ChangedBy: "tester2"})
		if err != nil {
			t.Fatalf("%s: unretract: %v", b.name, err)
		}
		if len(un.Impact.Restored) != 1 || un.Impact.Restored[0].ID != "cl-rx-only" || len(un.Impact.Relinked) != 1 {
			t.Errorf("%s: un-retraction impact = %+v, want cl-rx-only restored and cl-rx-shared relinked", b.name, un.Impact)
		}
		restored, err := b.conn.Claims.ListByIDs(ctx, []string{"cl-rx-only"})
		if err != nil || len(restored) != 1 || restored[0].Status != domain.ClaimStatusActive {
			t.Errorf("%s: cl-rx-only after un-retraction = %+v (%v), want active", b.name, restored, err)
		}
		if links, err := b.conn.Claims.ListEvidenceByClaimIDs(ctx, []string{"cl-rx-shared"}); err != nil || len(links) != 2 {
			t.Errorf("%s: cl-rx-shared evidence after un-retraction = %+v (%v), want both episodes", b.name, links, err)
		}
		if _, ok, err := govwrite.LookupRetraction(ctx, b.conn, "ev-rx-gone"); err != nil || ok {
			t.Errorf("%s: still retracted after un-retraction: ok=%v err=%v", b.name, ok, err)
		}
		if repeat, err := w.UnretractEvent(ctx, "ev-rx-gone", domain.ChangeNote{}); err != nil || !repeat.AlreadyUnretracted {
			t.Errorf("%s: repeat un-retraction = %+v, %v; want AlreadyUnretracted", b.name, repeat, err)
		}
	}
}

// ListBySourcePath finds a file's events by the metadata file ingestion stamps,
// on every backend (ports.EventSourceLister, used by watch_file).
func TestListBySourcePath_AgreesAcrossBackends(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, b := range openBackends(t) {
		lister, ok := b.conn.Events.(ports.EventSourceLister)
		if !ok {
			t.Errorf("%s: events repository does not implement EventSourceLister", b.name)
			continue
		}
		for i, ev := range []struct{ id, path string }{
			{"ev-sp-1", "/docs/a.md"}, {"ev-sp-2", "/docs/b.md"}, {"ev-sp-3", "/docs/a.md"}, {"ev-sp-4", ""},
		} {
			meta := map[string]string{"chunk_index": "0"}
			if ev.path != "" {
				meta["input_source_path"] = ev.path
			}
			if err := b.conn.Events.Append(ctx, domain.Event{
				ID: ev.id, RunID: "sp", Content: "chunk " + ev.id, SchemaVersion: "v1", SourceInputID: "src",
				Timestamp: at, IngestedAt: at.Add(time.Duration(i) * time.Second), Metadata: meta,
			}); err != nil {
				t.Fatalf("%s: append %s: %v", b.name, ev.id, err)
			}
		}
		got, err := lister.ListBySourcePath(ctx, "/docs/a.md")
		if err != nil {
			t.Fatalf("%s: list: %v", b.name, err)
		}
		if len(got) != 2 || got[0].ID != "ev-sp-1" || got[1].ID != "ev-sp-3" {
			ids := make([]string, len(got))
			for i, e := range got {
				ids[i] = e.ID
			}
			t.Errorf("%s: events of /docs/a.md = %v, want [ev-sp-1 ev-sp-3]", b.name, ids)
		}
	}
}

func answerHas(a domain.Answer, id string) bool {
	for _, c := range a.Claims {
		if c.ID == id {
			return true
		}
	}
	return false
}

func answerIDs(a domain.Answer) []string {
	out := make([]string, len(a.Claims))
	for i, c := range a.Claims {
		out[i] = c.ID
	}
	return out
}
