package main

import (
	"strings"
	"testing"
)

// At high contradiction counts the warning is the most useful line in the
// block, and it used to sit under six high-trust claims where it read as a
// footnote. Real sessions carried counts of 107, 157 and 485 while the reader
// took the claims at face value.
func TestRenderRecall_LeadsWithHighContradictionCount(t *testing.T) {
	claims := []recallClaim{
		{Type: "fact", Text: "warden is not installed", TrustScore: 0.8},
		{Type: "fact", Text: "briefkasten is HTTP-only", TrustScore: 0.75},
	}
	out := renderRecall(claims, 485)

	warnIdx := strings.Index(out, "heavily contested")
	claimsIdx := strings.Index(out, "Relevant knowledge")
	if warnIdx < 0 {
		t.Fatalf("no lead warning at 485 contradictions:\n%s", out)
	}
	if warnIdx > claimsIdx {
		t.Errorf("warning still appears below the claims:\n%s", out)
	}
	if !strings.Contains(out, "485") {
		t.Errorf("lead warning omits the count:\n%s", out)
	}
}

// An ordinary topic carries a handful of contradictions; that should stay a
// footnote rather than shouting on every prompt.
func TestRenderRecall_LowCountStaysAFootnote(t *testing.T) {
	claims := []recallClaim{{Type: "fact", Text: "the retry budget is 3", TrustScore: 0.9}}
	out := renderRecall(claims, 2)

	if strings.Contains(out, "heavily contested") {
		t.Errorf("escalated a 2-contradiction topic:\n%s", out)
	}
	warnIdx := strings.Index(out, "contradiction(s) recorded")
	claimsIdx := strings.Index(out, "Relevant knowledge")
	if warnIdx < 0 || warnIdx < claimsIdx {
		t.Errorf("expected the footnote form below the claims:\n%s", out)
	}
}

// A contested claim must be identifiable, not merely counted in an aggregate.
func TestRenderRecall_MarksContestedClaims(t *testing.T) {
	claims := []recallClaim{
		{Type: "fact", Text: "settled thing", TrustScore: 0.9},
		{Type: "fact", Text: "disputed thing", TrustScore: 0.9, Contested: true},
	}
	out := renderRecall(claims, 1)
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "disputed thing") && !strings.Contains(line, "contested") {
			t.Errorf("contested claim rendered without a marker: %q", line)
		}
		if strings.Contains(line, "settled thing") && strings.Contains(line, "contested") {
			t.Errorf("uncontested claim marked contested: %q", line)
		}
	}
}

// Only six claims are shown, so ordering decides what the reader sees. A
// contested claim must not displace a settled one from that window.
func TestDemoteContested_SettledClaimsKeepTheWindow(t *testing.T) {
	in := []recallClaim{
		{Text: "contested a", Contested: true},
		{Text: "settled 1"},
		{Text: "contested b", Contested: true},
		{Text: "settled 2"},
	}
	got := demoteContested(in)
	if len(got) != 4 {
		t.Fatalf("lost claims: %d of 4", len(got))
	}
	if got[0].Text != "settled 1" || got[1].Text != "settled 2" {
		t.Errorf("settled claims not promoted: %v", got)
	}
	if !got[2].Contested || !got[3].Contested {
		t.Errorf("contested claims not demoted: %v", got)
	}
}

// Tier precedence (ADR 0011 repo-wins) is a deliberate ordering; demotion must
// preserve relative order within each group rather than re-sorting.
func TestDemoteContested_PreservesRelativeOrder(t *testing.T) {
	in := []recallClaim{
		{Text: "repo", Source: "workspace"},
		{Text: "global", Source: "global"},
	}
	got := demoteContested(in)
	if got[0].Text != "repo" || got[1].Text != "global" {
		t.Errorf("tier precedence disturbed: %v", got)
	}
}
