package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/narration"
)

// fakeJudge drops the texts it is told to and keeps the rest. It can also fail
// outright, or answer only the first n texts of a call before reporting that
// the budget ran out.
type fakeJudge struct {
	drop    map[string]bool
	err     error
	answerN int // >0: answer only this many, then return context.DeadlineExceeded
	asked   []string
}

func (f *fakeJudge) Classify(_ context.Context, texts []string) ([]narration.Verdict, error) {
	f.asked = append(f.asked, texts...)
	out := make([]narration.Verdict, len(texts))
	if f.err != nil {
		return out, f.err
	}
	for i, t := range texts {
		if f.answerN > 0 && i >= f.answerN {
			return out, context.DeadlineExceeded
		}
		if f.drop[t] {
			out[i] = narration.Drop
		} else {
			out[i] = narration.Keep
		}
	}
	return out, nil
}

func (f *fakeJudge) Identity() string { return "fake|judge narration/v1" }

func captured(id, text string, status domain.ClaimStatus, at time.Time) domain.Claim {
	return domain.Claim{ID: id, Text: text, Status: status, CreatedBy: captureActor, CreatedAt: at}
}

// Only active or contested claims written by the capture hook are reviewed: a
// document, a deliberate record or an already-deprecated claim is never
// second-guessed. Order is oldest first, and --limit caps it.
func TestSelectJudgeCandidates(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	claims := []domain.Claim{
		captured("late", "b", domain.ClaimStatusActive, t0.Add(2*time.Hour)),
		captured("early", "a", domain.ClaimStatusContested, t0),
		captured("gone", "c", domain.ClaimStatusDeprecated, t0),
		{ID: "doc", Text: "d", Status: domain.ClaimStatusActive, CreatedBy: "ingest:handbook.md", CreatedAt: t0},
	}
	got := selectJudgeCandidates(claims, 0)
	if len(got) != 2 || got[0].ID != "early" || got[1].ID != "late" {
		t.Fatalf("candidates = %v, want [early late]", claimIDs(got))
	}
	if got := selectJudgeCandidates(claims, 1); len(got) != 1 || got[0].ID != "early" {
		t.Errorf("limit 1 = %v, want [early]", claimIDs(got))
	}
}

func claimIDs(cs []domain.Claim) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

// A dry run judges but writes nothing.
func TestJudgePassDryRunWritesNothing(t *testing.T) {
	cands := []domain.Claim{captured("1", "Both PRs are green", domain.ClaimStatusActive, time.Now())}
	writes := 0
	stats, err := runNarrationJudgePass(context.Background(), &fakeJudge{drop: map[string]bool{"Both PRs are green": true}},
		cands, true, func(context.Context, []domain.Claim) error { writes++; return nil }, io.Discard)
	if err != nil || writes != 0 || stats.Dropped != 1 || stats.Written != 0 {
		t.Fatalf("dry run: err=%v writes=%d stats=%+v", err, writes, stats)
	}
}

// The apply writes one transaction per chunk, deprecating only Drop verdicts,
// so the write lock is never held for the whole pass.
func TestJudgePassWritesPerChunkAndOnlyDrops(t *testing.T) {
	var cands []domain.Claim
	drop := map[string]bool{}
	for i := range judgeChunk + 5 {
		text := fmt.Sprintf("The billing service %d reads its listen port from the environment", i)
		if i%3 == 0 {
			drop[text] = true
		}
		cands = append(cands, captured(fmt.Sprint(i), text, domain.ClaimStatusActive, time.Now()))
	}
	var batches [][]domain.Claim
	stats, err := runNarrationJudgePass(context.Background(), &fakeJudge{drop: drop}, cands, false,
		func(_ context.Context, b []domain.Claim) error { batches = append(batches, b); return nil }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 {
		t.Fatalf("wrote %d transactions, want one per chunk (2)", len(batches))
	}
	n := 0
	for _, b := range batches {
		for _, c := range b {
			n++
			if !drop[c.Text] {
				t.Errorf("deprecated %q, which the judge kept", c.Text)
			}
			if c.Status != domain.ClaimStatusDeprecated {
				t.Errorf("%q written with status %q, want deprecated", c.Text, c.Status)
			}
		}
	}
	if n != len(drop) || stats.Written != len(drop) || stats.Reviewed != len(cands) {
		t.Errorf("deprecated %d (stats %+v), want %d of %d", n, stats, len(drop), len(cands))
	}
}

// A judge that fails keeps everything; only what the rule-based filter already
// calls junk is deprecated without it.
func TestJudgePassKeepsEverythingTheJudgeCouldNotAnswer(t *testing.T) {
	junk := "Let me check the config"
	if !extract.IsJunk(junk) {
		t.Fatalf("fixture %q is no longer rule-based junk; pick another", junk)
	}
	cands := []domain.Claim{
		captured("1", "drop_console strips console.log in production", domain.ClaimStatusActive, time.Now()),
		captured("2", junk, domain.ClaimStatusActive, time.Now()),
	}
	var written []domain.Claim
	_, err := runNarrationJudgePass(context.Background(), &fakeJudge{err: fmt.Errorf("connection refused")}, cands, false,
		func(_ context.Context, b []domain.Claim) error { written = append(written, b...); return nil }, io.Discard)
	if err == nil {
		t.Fatal("a judge failure other than the budget must surface")
	}
	for _, c := range written {
		if c.ID == "1" {
			t.Error("deprecated a claim the judge never answered for")
		}
	}

	// Unknown verdicts (no error, no answer) keep the claim as well.
	written = nil
	if _, err := runNarrationJudgePass(context.Background(), unknownJudge{}, cands[:1], false,
		func(_ context.Context, b []domain.Claim) error { written = append(written, b...); return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Errorf("an Unknown verdict deprecated %v", claimIDs(written))
	}
	stats, _ := runNarrationJudgePass(context.Background(), unknownJudge{}, cands[:1], true,
		func(context.Context, []domain.Claim) error { return nil }, io.Discard)
	if stats.Unanswered != 1 {
		t.Errorf("unanswered = %d, want 1: a claim with no verdict must be counted, not silently kept", stats.Unanswered)
	}
}

type unknownJudge struct{}

func (unknownJudge) Classify(_ context.Context, texts []string) ([]narration.Verdict, error) {
	return make([]narration.Verdict, len(texts)), nil
}
func (unknownJudge) Identity() string { return "unknown" }

// Running out of budget mid-chunk keeps what was earned: the verdicts that
// landed are written and the pass stops cleanly.
func TestJudgePassStopsAtTheBudgetAndKeepsWhatItEarned(t *testing.T) {
	var cands []domain.Claim
	drop := map[string]bool{}
	for i := range 30 {
		text := fmt.Sprintf("The release pipeline %d publishes signed images to the registry", i)
		drop[text] = true
		cands = append(cands, captured(fmt.Sprint(i), text, domain.ClaimStatusActive, time.Now()))
	}
	var written []domain.Claim
	stats, err := runNarrationJudgePass(context.Background(), &fakeJudge{drop: drop, answerN: 10}, cands, false,
		func(_ context.Context, b []domain.Claim) error { written = append(written, b...); return nil }, io.Discard)
	if err != nil {
		t.Fatalf("running out of budget is not a failure: %v", err)
	}
	if !stats.Partial || len(written) != 10 {
		t.Errorf("partial=%v written=%d, want a partial pass that wrote the 10 answered drops", stats.Partial, len(written))
	}
}

// End to end through the governed writer: a dropped claim is deprecated, not
// deleted, and its transition names the judge.
func TestJudgePassDeprecatesWithHistory(t *testing.T) {
	_, conn := openTestStore(t)
	w := wrapTestWriter(t, conn)
	ctx := context.Background()
	now := time.Now().UTC()
	seed := []domain.Claim{
		{ID: "narr", Text: "Both PRs are green", Type: domain.ClaimTypeFact, Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now, CreatedBy: captureActor},
		{ID: "fact", Text: "drop_console strips console.log in production", Type: domain.ClaimTypeFact, Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now, CreatedBy: captureActor},
	}
	if _, err := w.Claims(ctx, seed, govwrite.ClaimReason{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	judge := &fakeJudge{drop: map[string]bool{"Both PRs are green": true}}
	reason := govwrite.ClaimReason{Reason: "Session narration, not knowledge (judge " + judge.Identity() + ")", ChangedBy: "tester"}
	write := func(ctx context.Context, b []domain.Claim) error { _, err := w.Claims(ctx, b, reason); return err }
	all, err := conn.Claims.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runNarrationJudgePass(ctx, judge, selectJudgeCandidates(all, 0), false, write, io.Discard); err != nil {
		t.Fatal(err)
	}

	after, err := conn.Claims.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]domain.ClaimStatus{}
	for _, c := range after {
		status[c.ID] = c.Status
	}
	if status["narr"] != domain.ClaimStatusDeprecated || status["fact"] != domain.ClaimStatusActive {
		t.Fatalf("statuses = %v, want narr deprecated and fact active (and both still present)", status)
	}
	hist, err := conn.Claims.ListStatusHistoryByClaimID(ctx, "narr")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range hist {
		if h.ToStatus == domain.ClaimStatusDeprecated && strings.Contains(h.Reason, judge.Identity()) {
			found = true
		}
	}
	if !found {
		t.Errorf("no deprecation transition naming the judge in %+v", hist)
	}
}

func TestParsePruneOptions(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    pruneOptions
		wantErr bool
	}{
		{"rule pass unchanged", []string{"--narration"}, pruneOptions{target: "narration"}, false},
		{"judged", []string{"--narration", "--judge"}, pruneOptions{target: "narration", judge: true}, false},
		{"judged with limit", []string{"--narration", "--judge", "--limit", "500"}, pruneOptions{target: "narration", judge: true, limit: 500}, false},
		{"judge needs narration", []string{"--session-noise", "--judge"}, pruneOptions{}, true},
		{"limit needs judge", []string{"--narration", "--limit", "5"}, pruneOptions{}, true},
		{"limit needs a number", []string{"--narration", "--judge", "--limit"}, pruneOptions{}, true},
		{"limit must be positive", []string{"--narration", "--judge", "--limit", "0"}, pruneOptions{}, true},
		{"bare judge is still no target", []string{"--judge"}, pruneOptions{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePruneOptions(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("options = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// At capture, dropped claims leave with their evidence and entities; kept
// claims keep theirs.
func TestFilterSessionNarrationDropsClaimWithItsLinks(t *testing.T) {
	claims := []domain.Claim{{ID: "narr", Text: "Both PRs are green"}, {ID: "fact", Text: "drop_console strips console.log"}}
	links := []domain.ClaimEvidence{{ClaimID: "narr", EventID: "e1"}, {ClaimID: "fact", EventID: "e1"}}
	ents := map[string][]extract.ExtractedEntity{"narr": {{Name: "PR"}}, "fact": {{Name: "drop_console"}}}
	gotC, gotL, gotE := filterSessionNarration(context.Background(), &fakeJudge{drop: map[string]bool{"Both PRs are green": true}}, claims, links, ents)
	if len(gotC) != 1 || gotC[0].ID != "fact" {
		t.Fatalf("claims = %v, want [fact]", claimIDs(gotC))
	}
	if len(gotL) != 1 || gotL[0].ClaimID != "fact" {
		t.Errorf("links = %+v, want only fact's", gotL)
	}
	if _, ok := gotE["narr"]; ok || len(gotE["fact"]) != 1 {
		t.Errorf("entities = %+v, want only fact's", gotE)
	}
}

// Capture fails open: a judge error, or too little budget to ask, keeps every
// claim.
func TestFilterSessionNarrationFailsOpen(t *testing.T) {
	claims := []domain.Claim{{ID: "a", Text: "Both PRs are green"}}
	links := []domain.ClaimEvidence{{ClaimID: "a", EventID: "e"}}
	dropAll := map[string]bool{"Both PRs are green": true}

	if got, _, _ := filterSessionNarration(context.Background(), &fakeJudge{drop: dropAll, err: fmt.Errorf("model not found")}, claims, links, nil); len(got) != 1 {
		t.Error("a judge error dropped a claim")
	}
	short, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	j := &fakeJudge{drop: dropAll}
	if got, _, _ := filterSessionNarration(short, j, claims, links, nil); len(got) != 1 || len(j.asked) != 0 {
		t.Errorf("with 5s left the judge was asked (%v) or dropped a claim", j.asked)
	}
	if got, _, _ := filterSessionNarration(context.Background(), nil, claims, links, nil); len(got) != 1 {
		t.Error("no judge dropped a claim")
	}
}
