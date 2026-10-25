package pipeline

import (
	"context"
	"testing"
)

// ADR 0034: a belief archived for going unreinforced is still held. Restating
// it folds onto the archived claim, as onto any live one, and the write that
// reinforces it brings it back. Otherwise the restatement would land on a
// belief recall cannot see.
func TestPersistArtifacts_RestatementUnarchives(t *testing.T) {
	for name, conn := range knownClaimStores(t) {
		persistOne(t, conn, captureEvent("ev-a1", "session-1", "The retry budget is 3 attempts."),
			knownClaim("cl-held", "The retry budget is 3 attempts"))
		if err := conn.Claims.Archive(context.Background(), "cl-held", knownAt); err != nil {
			t.Fatalf("%s: archive: %v", name, err)
		}

		persistOne(t, conn, captureEvent("ev-a2", "session-2", "the retry budget is 3 attempts"),
			knownClaim("cl-restated", "the retry budget is 3 attempts"))

		if n := claimCount(t, conn); n != 1 {
			t.Errorf("%s: %d claims after restating an archived belief, want 1", name, n)
		}
		got, err := conn.Claims.ListByIDs(context.Background(), []string{"cl-held"})
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: read cl-held: %v", name, err)
		}
		if got[0].IsArchived() {
			t.Errorf("%s: the restated belief is still archived", name)
		}
		if ev := evidenceEvents(t, conn, "cl-held"); len(ev) != 2 {
			t.Errorf("%s: evidence = %v, want both sessions", name, ev)
		}
	}
}
