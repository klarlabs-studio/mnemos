package main

import (
	"os"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestUnjudgedIDs(t *testing.T) {
	claims := []recallClaim{
		{ID: "a"}, // no verdict — worth classifying
		{ID: "b", Judged: true, SessionLocal: true}, // already judged
		{ID: "c", Judged: true},                     // judged durable, also done
		{ID: ""},                                    // hosted/merged rows can lack an id
		{ID: "d"},
	}
	got := unjudgedIDs(claims, 0)
	if len(got) != 2 || got[0] != "a" || got[1] != "d" {
		t.Fatalf("got %v, want [a d]", got)
	}
}

// "Judged durable" must not be mistaken for "unjudged": both have
// SessionLocal=false, and re-classifying them would pay twice for the same
// answer on every prompt.
func TestUnjudgedIDs_DurableIsNotUnjudged(t *testing.T) {
	if got := unjudgedIDs([]recallClaim{{ID: "x", Judged: true}}, 0); len(got) != 0 {
		t.Fatalf("a judged-durable belief must not be re-classified, got %v", got)
	}
}

// One prompt must not be able to queue an unbounded amount of model work.
func TestUnjudgedIDs_RespectsCap(t *testing.T) {
	claims := make([]recallClaim, 50)
	for i := range claims {
		claims[i] = recallClaim{ID: string(rune('a' + i%26))}
	}
	if got := unjudgedIDs(claims, 3); len(got) != 3 {
		t.Fatalf("cap not applied, got %d", len(got))
	}
}

func TestRecallClassifyDue(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	now := time.Now()
	if !recallClassifyDue(now) {
		t.Fatal("a missing stamp must read as due")
	}
	markRecallClassify(now)
	if recallClassifyDue(now.Add(time.Minute)) {
		t.Fatal("a minute later is inside the throttle window")
	}
	if !recallClassifyDue(now.Add(recallClassifyInterval + time.Second)) {
		t.Fatal("past the interval must be due")
	}
}

// Failing open costs one extra spawn; failing closed would silence the fill-in
// permanently on a path we can never write.
func TestRecallClassifyDue_UnusableStampFailsOpen(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := os.MkdirAll(recallClassifyStamp(), 0o750); err != nil {
		t.Fatal(err)
	}
	if !recallClassifyDue(time.Now()) {
		t.Fatal("an unusable stamp must read as due")
	}
}

func TestMaybeClassifyRecalled_NothingToDoDoesNotStamp(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if maybeClassifyRecalled(nil, time.Now()) {
		t.Fatal("an empty id list must not spawn")
	}
	if _, err := os.Stat(recallClassifyStamp()); err == nil {
		t.Fatal("a no-op must not consume the throttle window")
	}
}

// A hosted session reads a remote brain; classifying the local store would
// write verdicts for beliefs the reader never saw.
func TestMaybeClassifyRecalled_SkipsHostedBrains(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("MNEMOS_URL", "https://brain.example.com")
	if maybeClassifyRecalled([]string{"a"}, time.Now()) {
		t.Fatal("hosted sessions must not classify the local store")
	}
}

func TestUnclassifiedAmong(t *testing.T) {
	claims := []domain.Claim{
		{ID: "a", CreatedAt: day(1)},
		{ID: "b", CreatedAt: day(1), Durability: domain.DurabilityDurable},
		{ID: "c", CreatedAt: day(1), Status: domain.ClaimStatusDeprecated},
	}
	// Order follows the caller's list; already-judged, deprecated and unknown
	// ids drop out, so a stale list from an older brief costs nothing.
	got := unclassifiedAmong(claims, []string{"a", "b", "c", "missing", "a"})
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("got %v, want just [a]", ids(got))
	}
}
