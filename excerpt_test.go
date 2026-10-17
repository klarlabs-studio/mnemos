package mnemos

import (
	"context"
	"strings"
	"testing"
	"time"
)

const conversation = `Caroline: I went to a lovely LGBTQ support group on 7 May 2023.
Melanie: That sounds wonderful, how did it feel?
Caroline: Honestly freeing. I met two people who also paint.
Melanie: I took the kids camping at the lake last weekend.`

// The excerpt is the source line the belief came from, so an answer can use
// the wording the belief dropped — here, the date.
func TestBestExcerpt_PicksTheSourceLine(t *testing.T) {
	got := bestExcerpt("Caroline attends an LGBTQ support group", []string{conversation}, maxExcerptChars)
	if !strings.Contains(got, "7 May 2023") {
		t.Fatalf("excerpt = %q, want the line with the date", got)
	}
	if strings.Contains(got, "camping") {
		t.Errorf("excerpt = %q, reached a line unrelated to the belief", got)
	}
}

// Neighbouring lines are added while they fit, so a reply that completes the
// source line comes with it.
func TestBestExcerpt_AddsNeighboursWithinTheCap(t *testing.T) {
	got := bestExcerpt("Caroline met two people who also paint", []string{conversation}, 200)
	if !strings.Contains(got, "also paint") {
		t.Fatalf("excerpt = %q, want the source line", got)
	}
	if len(got) > 200 {
		t.Errorf("excerpt is %d chars, want at most 200", len(got))
	}
	if !strings.Contains(got, "\n") {
		t.Errorf("excerpt = %q, want a neighbouring line within the cap", got)
	}
}

// One long paragraph is split into sentences, and a sentence longer than the
// cap is cut rather than returned whole.
func TestBestExcerpt_SplitsProseAndRespectsTheCap(t *testing.T) {
	prose := "The deploy ran on Tuesday. " + strings.Repeat("Nothing else happened. ", 30) +
		"The rollback to v1.4 finished at 14:05 after the health check failed."
	got := bestExcerpt("the rollback finished after the health check failed", []string{prose}, 80)
	if !strings.Contains(got, "rollback") {
		t.Fatalf("excerpt = %q, want the rollback sentence", got)
	}
	if len(got) > 80 {
		t.Errorf("excerpt is %d chars, want at most 80", len(got))
	}
}

func TestBestExcerpt_NoSourceText(t *testing.T) {
	if got := bestExcerpt("anything", nil, maxExcerptChars); got != "" {
		t.Errorf("excerpt = %q, want empty with no source", got)
	}
	if got := bestExcerpt("anything", []string{"unrelated words only"}, maxExcerptChars); got != "" {
		t.Errorf("excerpt = %q, want empty when nothing in the source overlaps", got)
	}
}

// Through the public API: asking for excerpts returns the source wording with
// each belief; not asking leaves Recall's output as it was.
func TestRecall_ReturnsSourceExcerptsWhenAsked(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := m.RememberEvent(ctx, Event{ID: "ev-conv", At: now, Type: "conversation", Content: conversation}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}
	if _, err := m.RememberClaim(ctx, ClaimItem{Text: "Caroline attends an LGBTQ support group", EventIDs: []string{"ev-conv"}, ValidFrom: now}); err != nil {
		t.Fatalf("RememberClaim: %v", err)
	}

	plain, err := m.Recall(ctx, Query{Text: "when did Caroline go to the support group"})
	if err != nil || len(plain) == 0 {
		t.Fatalf("Recall: %v, %d results", err, len(plain))
	}
	if plain[0].Excerpt != "" {
		t.Errorf("Excerpt = %q without Query.Excerpts, want empty", plain[0].Excerpt)
	}

	withSrc, err := m.Recall(ctx, Query{Text: "when did Caroline go to the support group", Excerpts: true})
	if err != nil || len(withSrc) == 0 {
		t.Fatalf("Recall with excerpts: %v, %d results", err, len(withSrc))
	}
	if !strings.Contains(withSrc[0].Excerpt, "7 May 2023") {
		t.Errorf("Excerpt = %q, want the source line with the date", withSrc[0].Excerpt)
	}
}
