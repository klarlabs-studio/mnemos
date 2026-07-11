package main

import (
	"strings"
	"testing"
)

func TestUpsertManagedBlock_InsertsIntoEmpty(t *testing.T) {
	got := upsertManagedBlock("", "hello")
	if !strings.Contains(got, docBeginMarker) || !strings.Contains(got, docEndMarker) || !strings.Contains(got, "hello") {
		t.Fatalf("empty upsert wrong:\n%s", got)
	}
}

func TestUpsertManagedBlock_PreservesHumanContent(t *testing.T) {
	human := "# My Project\n\nHand-written guidance the human owns.\n"
	got := upsertManagedBlock(human, "generated learnings")
	if !strings.Contains(got, "Hand-written guidance the human owns.") {
		t.Errorf("human content lost:\n%s", got)
	}
	if !strings.Contains(got, "generated learnings") {
		t.Errorf("block not inserted:\n%s", got)
	}
	if !strings.HasPrefix(got, "# My Project") {
		t.Errorf("human content should stay at the top:\n%s", got)
	}
}

func TestUpsertManagedBlock_ReplacesInPlaceAndIsIdempotent(t *testing.T) {
	human := "# Title\n\nbefore\n\n" + docBeginMarker + "\nOLD\n" + docEndMarker + "\n\nafter\n"
	got := upsertManagedBlock(human, "NEW")
	if strings.Contains(got, "OLD") {
		t.Errorf("old block not replaced:\n%s", got)
	}
	if !strings.Contains(got, "NEW") || !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("surrounding content or new block wrong:\n%s", got)
	}
	// Idempotent: re-applying the same block yields identical output.
	if again := upsertManagedBlock(got, "NEW"); again != got {
		t.Errorf("not idempotent:\nfirst:\n%s\nsecond:\n%s", got, again)
	}
	// Exactly one managed block.
	if strings.Count(got, docBeginMarker) != 1 || strings.Count(got, docEndMarker) != 1 {
		t.Errorf("expected exactly one managed block:\n%s", got)
	}
}
