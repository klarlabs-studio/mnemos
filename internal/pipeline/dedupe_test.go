package pipeline

import (
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestDedupeAgainstExisting_RemapsToExistingClaim(t *testing.T) {
	existing := []domain.Claim{
		{ID: "cl_old", Text: "Felix needs coffee for the flea market"},
	}
	newClaims := []domain.Claim{
		{ID: "cl_new1", Text: "Felix needs coffee for the flea market."}, // trailing punct
		{ID: "cl_new2", Text: "Cake tongs are required"},
	}
	links := []domain.ClaimEvidence{
		{ClaimID: "cl_new1", EventID: "ev1"},
		{ClaimID: "cl_new2", EventID: "ev1"},
	}

	gotClaims, gotLinks := DedupeAgainstExisting(newClaims, links, existing)

	if len(gotClaims) != 1 || gotClaims[0].ID != "cl_new2" {
		t.Fatalf("expected only cl_new2 to remain, got %+v", gotClaims)
	}

	// The link for cl_new1 should be remapped to cl_old, and the
	// link for cl_new2 should pass through unchanged.
	wantLinks := map[string]string{
		"cl_old":  "ev1",
		"cl_new2": "ev1",
	}
	if len(gotLinks) != len(wantLinks) {
		t.Fatalf("links length = %d, want %d: %+v", len(gotLinks), len(wantLinks), gotLinks)
	}
	for _, l := range gotLinks {
		if wantLinks[l.ClaimID] != l.EventID {
			t.Fatalf("unexpected link %+v", l)
		}
	}
}

func TestDedupeAgainstExisting_CollapsesWithinBatch(t *testing.T) {
	newClaims := []domain.Claim{
		{ID: "cl_a", Text: "Revenue grew 15% in Q3"},
		{ID: "cl_b", Text: "  Revenue grew 15% in Q3.  "}, // same fact, trivial differences
		{ID: "cl_c", Text: "Margins improved"},
	}
	links := []domain.ClaimEvidence{
		{ClaimID: "cl_a", EventID: "ev1"},
		{ClaimID: "cl_b", EventID: "ev2"},
		{ClaimID: "cl_c", EventID: "ev3"},
	}

	gotClaims, gotLinks := DedupeAgainstExisting(newClaims, links, nil)

	if len(gotClaims) != 2 {
		t.Fatalf("expected 2 claims after intra-batch dedup, got %d: %+v", len(gotClaims), gotClaims)
	}
	// cl_a wins (first-seen) so cl_b's link should remap to cl_a's id.
	var sawA1, sawA2, sawC bool
	for _, l := range gotLinks {
		if l.ClaimID == "cl_a" && l.EventID == "ev1" {
			sawA1 = true
		}
		if l.ClaimID == "cl_a" && l.EventID == "ev2" {
			sawA2 = true
		}
		if l.ClaimID == "cl_c" && l.EventID == "ev3" {
			sawC = true
		}
	}
	if !sawA1 || !sawA2 || !sawC {
		t.Fatalf("missing remapped link; got %+v", gotLinks)
	}
}

func TestDedupeAgainstExisting_NoOpWhenNothingMatches(t *testing.T) {
	newClaims := []domain.Claim{
		{ID: "cl_x", Text: "fresh fact"},
	}
	links := []domain.ClaimEvidence{
		{ClaimID: "cl_x", EventID: "ev1"},
	}
	existing := []domain.Claim{
		{ID: "cl_other", Text: "unrelated old claim"},
	}

	gotClaims, gotLinks := DedupeAgainstExisting(newClaims, links, existing)
	if len(gotClaims) != 1 || gotClaims[0].ID != "cl_x" {
		t.Fatalf("expected cl_x to pass through, got %+v", gotClaims)
	}
	if len(gotLinks) != 1 || gotLinks[0].ClaimID != "cl_x" {
		t.Fatalf("links should be unchanged, got %+v", gotLinks)
	}
}

func TestDedupeAgainstExisting_DropsDuplicateLinks(t *testing.T) {
	// Two batch-internal duplicates pointing at the same event must
	// not produce two links to the canonical id.
	newClaims := []domain.Claim{
		{ID: "cl_a", Text: "same fact"},
		{ID: "cl_b", Text: "Same Fact"},
	}
	links := []domain.ClaimEvidence{
		{ClaimID: "cl_a", EventID: "ev1"},
		{ClaimID: "cl_b", EventID: "ev1"},
	}

	gotClaims, gotLinks := DedupeAgainstExisting(newClaims, links, nil)
	if len(gotClaims) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(gotClaims))
	}
	if len(gotLinks) != 1 {
		t.Fatalf("expected duplicate (claim_id, event_id) link to be collapsed, got %d: %+v", len(gotLinks), gotLinks)
	}
}
