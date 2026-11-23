package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Two workers for one session must never capture at once: the offset is saved
// only after a chunk is stored, so both would capture the same lines.
func TestCaptureLocksExcludeTheSameSession(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	first, err := tryCaptureLocks("s1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tryCaptureLocks("s1"); !errors.Is(err, errLockHeld) {
		t.Fatalf("second worker for the same session: err = %v, want errLockHeld", err)
	}
	first.release()
	again, err := tryCaptureLocks("s1")
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again.release()
}

// Across sessions, at most maxCaptureWorkers run; a refused worker takes no
// session lock with it, so that session's next worker is not blocked by it.
func TestCaptureLocksCapWorkersAcrossSessions(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var held []*captureLocks
	for i := range maxCaptureWorkers {
		l, err := tryCaptureLocks(string(rune('a' + i)))
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
		held = append(held, l)
	}
	if _, err := tryCaptureLocks("over"); !errors.Is(err, errLockHeld) {
		t.Fatalf("worker over the cap: err = %v, want errLockHeld", err)
	}
	held[0].release()
	l, err := tryCaptureLocks("over")
	if err != nil {
		t.Fatalf("a freed slot was not reusable, or the refused attempt kept its session lock: %v", err)
	}
	l.release()
	for _, h := range held[1:] {
		h.release()
	}
}

// The SessionEnd sweep waits for a running worker rather than skipping, and
// gives up when its budget ends.
func TestWaitCaptureLocks(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	busy, err := tryCaptureLocks("s")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(2 * captureLockPoll)
		busy.release()
	}()
	got, err := waitCaptureLocks(context.Background(), "s")
	if err != nil {
		t.Fatalf("wait: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), captureLockPoll)
	defer cancel()
	if _, err := waitCaptureLocks(ctx, "s"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait past the budget: err = %v, want DeadlineExceeded", err)
	}
	got.release()
}
