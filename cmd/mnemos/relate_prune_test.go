package main

import (
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func claim(id, text string) domain.Claim { return domain.Claim{ID: id, Text: text} }

// Only contradiction edges are re-derived. Supports/causal edges include ones
// the LLM augmentation path contributed, which the rule-based engine cannot
// reproduce offline — re-deriving them would silently delete real knowledge.
func TestPartitionStaleContradictions_LeavesNonContradictionEdgesAlone(t *testing.T) {
	byID := map[string]domain.Claim{
		"a": claim("a", "Now I'll build"),
		"b": claim("b", "Frontend builds clean"),
	}
	rels := []domain.Relationship{
		{ID: "r1", Type: domain.RelationshipTypeSupports, FromClaimID: "a", ToClaimID: "b"},
		{ID: "r2", Type: domain.RelationshipTypeCauses, FromClaimID: "a", ToClaimID: "b"},
	}
	keep, dropped, orphaned := partitionStaleContradictions(rels, byID)
	if len(dropped) != 0 || orphaned != 0 {
		t.Fatalf("non-contradiction edges were touched: dropped=%d orphaned=%d", len(dropped), orphaned)
	}
	if len(keep) != 2 {
		t.Errorf("kept %d edges, want 2", len(keep))
	}
}

// The production false positive: two narration fragments the current detectors
// no longer relate.
func TestPartitionStaleContradictions_DropsStaleEdge(t *testing.T) {
	byID := map[string]domain.Claim{
		"a": claim("a", "Now the overlay"),
		"b": claim("b", "Clear picture now"),
	}
	rels := []domain.Relationship{
		{ID: "r1", Type: domain.RelationshipTypeContradicts, FromClaimID: "a", ToClaimID: "b"},
	}
	keep, dropped, _ := partitionStaleContradictions(rels, byID)
	if len(dropped) != 1 {
		t.Fatalf("stale contradiction not dropped (kept %d)", len(keep))
	}
}

// A contradiction the detectors still produce must survive — pruning must not
// become "delete all contradictions".
func TestPartitionStaleContradictions_KeepsLiveContradiction(t *testing.T) {
	byID := map[string]domain.Claim{
		"a": claim("a", "the customer had 12 prior refunds"),
		"b": claim("b", "the customer had 0 prior refunds"),
	}
	rels := []domain.Relationship{
		{ID: "r1", Type: domain.RelationshipTypeContradicts, FromClaimID: "a", ToClaimID: "b"},
	}
	keep, dropped, _ := partitionStaleContradictions(rels, byID)
	if len(dropped) != 0 {
		t.Error("a still-valid contradiction was pruned")
	}
	if len(keep) != 1 {
		t.Errorf("kept %d edges, want 1", len(keep))
	}
}

// An edge whose endpoint claim is gone cannot be re-derived and must not be
// restored — it would be a dangling reference.
func TestPartitionStaleContradictions_DropsOrphanedEdges(t *testing.T) {
	byID := map[string]domain.Claim{"a": claim("a", "something")}
	rels := []domain.Relationship{
		{ID: "r1", Type: domain.RelationshipTypeContradicts, FromClaimID: "a", ToClaimID: "gone"},
		{ID: "r2", Type: domain.RelationshipTypeSupports, FromClaimID: "missing", ToClaimID: "a"},
	}
	keep, _, orphaned := partitionStaleContradictions(rels, byID)
	if orphaned != 2 {
		t.Errorf("orphaned = %d, want 2", orphaned)
	}
	if len(keep) != 0 {
		t.Errorf("kept %d dangling edges", len(keep))
	}
}
