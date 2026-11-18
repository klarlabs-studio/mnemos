package query

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// ADR 0036: a recorded-as-of read answers "what did the brain believe at T",
// not "which of today's beliefs existed at T".
//
// The scenario is the incident timeline from the competitive strategy: a deploy
// on Monday, an outage diagnosed on Tuesday, the root cause found on Thursday.
// On Thursday the Tuesday diagnosis is retracted, a contradiction is recorded,
// a runbook belief is archived by a sweep and a configuration fact is
// superseded. A Tuesday read must return the Tuesday state of every one of
// them; before ADR 0036 it returned Thursday's.
func TestRecordedAsOf_IncidentTimelineReconstructsTuesdayBelief(t *testing.T) {
	ctx := context.Background()
	conn, err := store.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "timeline.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	add := func(eventID, claimID, text string) {
		t.Helper()
		now := time.Now().UTC()
		ev := domain.Event{
			ID: eventID, RunID: "timeline", SchemaVersion: "v1", SourceInputID: "timeline",
			Content: text, Timestamp: now, IngestedAt: now,
		}
		if err := conn.Events.Append(ctx, ev); err != nil {
			t.Fatalf("append %s: %v", eventID, err)
		}
		cl := domain.Claim{
			ID: claimID, Text: text, Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive,
			Confidence: 0.8, TrustScore: 0.8, CreatedAt: now, ValidFrom: now.Add(-time.Hour),
		}
		if err := conn.Claims.Upsert(ctx, []domain.Claim{cl}); err != nil {
			t.Fatalf("upsert %s: %v", claimID, err)
		}
		if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: claimID, EventID: eventID}}); err != nil {
			t.Fatalf("evidence %s: %v", claimID, err)
		}
	}
	// tick separates the phases: every store write stamps its own clock, and
	// the phase boundary must fall strictly between two writes.
	tick := func() time.Time {
		time.Sleep(5 * time.Millisecond)
		at := time.Now().UTC()
		time.Sleep(5 * time.Millisecond)
		return at
	}

	// Monday: the deploy, a configuration fact, a runbook belief.
	add("ev-deploy", "cl-deploy", "payments outage context: the v2 deploy changed the payments connection pool")
	add("ev-pool", "cl-pool50", "payments outage context: the payments connection pool size is 50")
	add("ev-runbook", "cl-runbook", "payments outage runbook: restart the payments database first")
	// Tuesday: the first diagnosis.
	add("ev-incident", "cl-disk", "payments outage cause: database disk saturation")
	tuesday := tick()

	// Thursday: the root cause, and everything it changes.
	add("ev-rca", "cl-rca", "payments outage cause: the v2 connection pool change starved the payments database")
	disk, err := conn.Claims.ListByIDs(ctx, []string{"cl-disk"})
	if err != nil || len(disk) != 1 {
		t.Fatalf("load cl-disk: %v (%d)", err, len(disk))
	}
	disk[0].Status = domain.ClaimStatusDeprecated
	if err := conn.Claims.UpsertWithReason(ctx, disk, "retracted: root cause analysis"); err != nil {
		t.Fatalf("retract cl-disk: %v", err)
	}
	if err := conn.Relationships.Upsert(ctx, []domain.Relationship{{
		ID: "rel-rca-disk", Type: domain.RelationshipTypeContradicts,
		FromClaimID: "cl-rca", ToClaimID: "cl-disk", CreatedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("contradiction: %v", err)
	}
	if err := conn.Claims.Archive(ctx, "cl-runbook", time.Now().UTC(), domain.ChangeNote{Reason: "sweep"}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := conn.Claims.SetValidity(ctx, "cl-pool50", time.Now().UTC(), domain.ChangeNote{Reason: "pool resized"}); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	tick()

	engine := NewEngine(conn.Events.(eventLister), conn.Claims, conn.Relationships)
	const question = "payments outage cause connection pool database"

	ids := func(ans domain.Answer) map[string]domain.Claim {
		out := make(map[string]domain.Claim, len(ans.Claims))
		for _, c := range ans.Claims {
			out[c.ID] = c
		}
		return out
	}

	// Today: the Thursday picture.
	now, err := engine.AnswerWithOptions(ctx, question, AnswerOptions{})
	if err != nil {
		t.Fatalf("answer now: %v", err)
	}
	got := ids(now)
	if _, ok := got["cl-rca"]; !ok {
		t.Errorf("today's answer is missing the root cause")
	}
	for _, gone := range []string{"cl-disk", "cl-runbook", "cl-pool50"} {
		if _, ok := got[gone]; ok {
			t.Errorf("today's answer still returns %s", gone)
		}
	}

	// Tuesday, as the brain recorded it.
	opts := AnswerOptions{RecordedAsOf: tuesday, Hebbian: true, Reconsolidate: true, Inhibit: true}
	then, err := engine.AnswerWithOptions(ctx, question, opts)
	if err != nil {
		t.Fatalf("answer as of Tuesday: %v", err)
	}
	got = ids(then)
	if _, ok := got["cl-rca"]; ok {
		t.Errorf("Tuesday's answer contains the root cause recorded on Thursday")
	}
	for _, held := range []string{"cl-disk", "cl-runbook", "cl-pool50", "cl-deploy"} {
		c, ok := got[held]
		if !ok {
			t.Errorf("Tuesday's answer is missing %s, which the brain held on Tuesday", held)
			continue
		}
		if c.Status != domain.ClaimStatusActive {
			t.Errorf("%s reads as %q on Tuesday, want active", held, c.Status)
		}
		if !c.ValidTo.IsZero() || !c.ArchivedAt.IsZero() {
			t.Errorf("%s carries Thursday's valid_to=%v archived_at=%v", held, c.ValidTo, c.ArchivedAt)
		}
	}
	if len(then.Contradictions) != 0 {
		t.Errorf("Tuesday's answer shows %d contradiction(s) recorded on Thursday", len(then.Contradictions))
	}

	// A read of the past does not write the present (ADR 0036 §2).
	after, err := conn.Claims.ListByIDs(ctx, []string{"cl-disk", "cl-deploy"})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, c := range after {
		if c.VerifyCount != 0 || !c.LastVerified.IsZero() {
			t.Errorf("%s was reconsolidated by a historical read (verify_count=%d)", c.ID, c.VerifyCount)
		}
	}
	again, err := engine.AnswerWithOptions(ctx, question, opts)
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if len(again.Claims) != len(then.Claims) {
		t.Errorf("a repeated historical read returned %d claims, first returned %d", len(again.Claims), len(then.Claims))
	}
}

func TestRewindClaim(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC) // Monday
	tue := t0.Add(24 * time.Hour)
	thu := t0.Add(72 * time.Hour)
	base := domain.Claim{
		ID: "c", CreatedAt: t0, Status: domain.ClaimStatusDeprecated,
		ValidTo: thu, ArchivedAt: thu,
	}
	insert := domain.ClaimStatusTransition{ToStatus: domain.ClaimStatusActive, ChangedAt: t0}
	retract := domain.ClaimStatusTransition{FromStatus: domain.ClaimStatusActive, ToStatus: domain.ClaimStatusDeprecated, ChangedAt: thu}
	ended := domain.ValidityChange{Field: domain.ValidityFieldValidTo, To: thu, ChangedAt: thu}
	archived := domain.ValidityChange{Field: domain.ValidityFieldArchivedAt, To: thu, ChangedAt: thu}

	cases := []struct {
		name       string
		claim      domain.Claim
		status     []domain.ClaimStatusTransition
		validity   []domain.ValidityChange
		at         time.Time
		wantExists bool
		want       domain.Claim
	}{
		{
			name: "before the claim was recorded it does not exist", claim: base, at: t0.Add(-time.Hour),
		},
		{
			name: "every change after T is undone", claim: base, at: tue, wantExists: true,
			status:   []domain.ClaimStatusTransition{insert, retract},
			validity: []domain.ValidityChange{ended, archived},
			want:     domain.Claim{ID: "c", CreatedAt: t0, Status: domain.ClaimStatusActive},
		},
		{
			name: "changes at or before T are kept", claim: base, at: thu, wantExists: true,
			status:   []domain.ClaimStatusTransition{insert, retract},
			validity: []domain.ValidityChange{ended, archived},
			want:     base,
		},
		{
			name: "only later transitions: the first one's from-state held at T", claim: base, at: tue, wantExists: true,
			status: []domain.ClaimStatusTransition{retract},
			want:   domain.Claim{ID: "c", CreatedAt: t0, Status: domain.ClaimStatusActive, ValidTo: thu, ArchivedAt: thu},
		},
		{
			name: "no history: the stored value is the best evidence", claim: base, at: tue, wantExists: true,
			want: base,
		},
		{
			name: "an empty from-status never blanks a claim", claim: base, at: tue, wantExists: true,
			status: []domain.ClaimStatusTransition{{ToStatus: domain.ClaimStatusDeprecated, ChangedAt: thu}},
			want:   base,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := rewindClaim(tc.claim, tc.status, tc.validity, tc.at)
			if ok != tc.wantExists {
				t.Fatalf("exists = %v, want %v", ok, tc.wantExists)
			}
			if !ok {
				return
			}
			if got.Status != tc.want.Status || !got.ValidTo.Equal(tc.want.ValidTo) || !got.ArchivedAt.Equal(tc.want.ArchivedAt) {
				t.Errorf("got status=%q valid_to=%v archived_at=%v, want status=%q valid_to=%v archived_at=%v",
					got.Status, got.ValidTo, got.ArchivedAt, tc.want.Status, tc.want.ValidTo, tc.want.ArchivedAt)
			}
		})
	}
}
