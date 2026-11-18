package query

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/recorded"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
	"go.klarlabs.de/mnemos/internal/trust"
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

// timelineStore is a throwaway SQLite brain for the recorded-as-of scenarios.
type timelineStore struct {
	t    *testing.T
	ctx  context.Context
	conn *store.Conn
}

func openTimeline(t *testing.T) timelineStore {
	t.Helper()
	ctx := context.Background()
	conn, err := store.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "timeline.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return timelineStore{t: t, ctx: ctx, conn: conn}
}

// add records a belief and the episode it came from, now.
func (s timelineStore) add(eventID, claimID, text string) {
	s.t.Helper()
	now := time.Now().UTC()
	ev := domain.Event{
		ID: eventID, RunID: "timeline", SchemaVersion: "v1", SourceInputID: "timeline",
		Content: text, Timestamp: now, IngestedAt: now,
	}
	if err := s.conn.Events.Append(s.ctx, ev); err != nil {
		s.t.Fatalf("append %s: %v", eventID, err)
	}
	cl := domain.Claim{
		ID: claimID, Text: text, Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive,
		Confidence: 0.8, TrustScore: 0.8, CreatedAt: now, ValidFrom: now.Add(-time.Hour),
	}
	if err := s.conn.Claims.Upsert(s.ctx, []domain.Claim{cl}); err != nil {
		s.t.Fatalf("upsert %s: %v", claimID, err)
	}
	if err := s.conn.Claims.UpsertEvidence(s.ctx, []domain.ClaimEvidence{{ClaimID: claimID, EventID: eventID}}); err != nil {
		s.t.Fatalf("evidence %s: %v", claimID, err)
	}
}

// tick returns an instant strictly between the writes before and after it.
func tick() time.Time {
	time.Sleep(5 * time.Millisecond)
	at := time.Now().UTC()
	time.Sleep(5 * time.Millisecond)
	return at
}

// ADR 0036 step 2: a merge after T must not hide what the brain held at T.
//
// On Tuesday the brain holds the disk diagnosis and a belief contradicting it.
// On Thursday a restatement is recorded and semantic dedupe folds the Tuesday
// diagnosis into it: the diagnosis becomes a redirect, its evidence and its
// contradiction move onto the Thursday claim. A Tuesday read reaches the
// Thursday claim through the moved evidence and drops it (recorded after T);
// before step 2 the Tuesday diagnosis was therefore unreachable, and so was its
// contradiction.
func TestRecordedAsOf_MergeAfterTKeepsTheBeliefHeldAtT(t *testing.T) {
	s := openTimeline(t)
	ctx := s.ctx

	s.add("ev-disk", "cl-disk", "payments outage cause: database disk saturation")
	s.add("ev-iops", "cl-iops", "payments outage cause: database disk saturation is ruled out, iops were normal")
	if err := s.conn.Relationships.Upsert(ctx, []domain.Relationship{{
		ID: "rel-iops-disk", Type: domain.RelationshipTypeContradicts,
		FromClaimID: "cl-iops", ToClaimID: "cl-disk", CreatedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("contradiction: %v", err)
	}
	tuesday := tick()

	s.add("ev-restated", "cl-restated", "payments outage cause: saturation of the database disk")
	if _, err := pipeline.ApplySemanticDedupe(ctx, s.conn, pipeline.SemanticDedupePlan{
		Merges: []pipeline.SemanticMerge{{WinnerID: "cl-restated", DuplicateIDs: []string{"cl-disk"}}},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	tick()

	engine := NewEngine(s.conn.Events.(eventLister), s.conn.Claims, s.conn.Relationships)
	const question = "payments outage cause database disk saturation"

	now, err := engine.AnswerWithOptions(ctx, question, AnswerOptions{})
	if err != nil {
		t.Fatalf("answer now: %v", err)
	}
	if got := answerIDs(now); !got["cl-restated"] || got["cl-disk"] {
		t.Fatalf("today's answer = %v, want the merged belief on cl-restated only", got)
	}

	then, err := engine.AnswerWithOptions(ctx, question, AnswerOptions{RecordedAsOf: tuesday})
	if err != nil {
		t.Fatalf("answer as of Tuesday: %v", err)
	}
	got := answerIDs(then)
	if got["cl-restated"] {
		t.Errorf("Tuesday's answer contains cl-restated, recorded on Thursday")
	}
	if !got["cl-disk"] {
		t.Fatalf("Tuesday's answer = %v, missing cl-disk, which the brain held on Tuesday", got)
	}
	for _, c := range then.Claims {
		if c.ID == "cl-disk" && (c.Status != domain.ClaimStatusActive || c.MergedInto != "") {
			t.Errorf("cl-disk reads on Tuesday as status=%q merged_into=%q, want active and unmerged", c.Status, c.MergedInto)
		}
	}
	found := false
	for _, rel := range then.Contradictions {
		if rel.ID == "rel-iops-disk" {
			found = true
			if rel.FromClaimID != "cl-iops" || rel.ToClaimID != "cl-disk" {
				t.Errorf("Tuesday's contradiction reads %s→%s, want cl-iops→cl-disk as recorded", rel.FromClaimID, rel.ToClaimID)
			}
		}
	}
	if !found {
		t.Errorf("Tuesday's answer lost the contradiction cl-iops→cl-disk the merge moved onto cl-restated (got %+v)", then.Contradictions)
	}
}

// ADR 0036 §8: a merge closes the edges it drops instead of deleting them.
//
// On Wednesday the brain holds the disk diagnosis, its restatement, a support
// edge between the two, and a belief contradicting each. On Thursday the
// diagnosis is merged into the restatement: the support edge would become a
// self-loop and one contradiction a duplicate of the other, so the merge drops
// both. A Wednesday read must still return both contradictions, each on the
// belief it was recorded against.
func TestRecordedAsOf_MergeKeepsTheEdgesItDropped(t *testing.T) {
	s := openTimeline(t)
	ctx := s.ctx

	s.add("ev-disk", "cl-disk", "payments outage cause: database disk saturation")
	s.add("ev-restated", "cl-restated", "payments outage cause: saturation of the database disk")
	s.add("ev-iops", "cl-iops", "payments outage cause: database disk saturation is ruled out, iops were normal")
	now := time.Now().UTC()
	if err := s.conn.Relationships.Upsert(ctx, []domain.Relationship{
		{ID: "rel-iops-disk", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl-iops", ToClaimID: "cl-disk", CreatedAt: now},
		{ID: "rel-iops-restated", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl-iops", ToClaimID: "cl-restated", CreatedAt: now},
		{ID: "rel-restated-disk", Type: domain.RelationshipTypeSupports, FromClaimID: "cl-restated", ToClaimID: "cl-disk", CreatedAt: now},
	}); err != nil {
		t.Fatalf("edges: %v", err)
	}
	wednesday := tick()
	if _, err := pipeline.ApplySemanticDedupe(ctx, s.conn, pipeline.SemanticDedupePlan{
		Merges: []pipeline.SemanticMerge{{WinnerID: "cl-restated", DuplicateIDs: []string{"cl-disk"}}},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	live, err := s.conn.Relationships.ListAll(ctx)
	if err != nil {
		t.Fatalf("live edges: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("live edges after the merge = %+v, want the one surviving contradiction", live)
	}

	engine := NewEngine(s.conn.Events.(eventLister), s.conn.Claims, s.conn.Relationships)
	then, err := engine.AnswerWithOptions(ctx, "payments outage cause database disk saturation", AnswerOptions{RecordedAsOf: wednesday})
	if err != nil {
		t.Fatalf("answer as of Wednesday: %v", err)
	}
	got := map[string]string{}
	for _, rel := range then.Contradictions {
		got[rel.ID] = rel.FromClaimID + "→" + rel.ToClaimID
	}
	want := map[string]string{"rel-iops-disk": "cl-iops→cl-disk", "rel-iops-restated": "cl-iops→cl-restated"}
	for id, ends := range want {
		if got[id] != ends {
			t.Errorf("Wednesday contradiction %s = %q, want %q (all: %v)", id, got[id], ends, got)
		}
	}

	edges, err := recordedRelationships(s.conn.Relationships, s.conn.Claims, wednesday).ListByClaimIDs(ctx, []string{"cl-disk"})
	if err != nil {
		t.Fatalf("recorded edges of cl-disk: %v", err)
	}
	support := false
	for _, rel := range edges {
		if rel.ID == "rel-restated-disk" && rel.FromClaimID == "cl-restated" && rel.ToClaimID == "cl-disk" {
			support = true
		}
	}
	if !support {
		t.Errorf("Wednesday edges of cl-disk = %+v, missing the support edge the merge dropped as a self-loop", edges)
	}
}

func answerIDs(ans domain.Answer) map[string]bool {
	out := make(map[string]bool, len(ans.Claims))
	for _, c := range ans.Claims {
		out[c.ID] = true
	}
	return out
}

// ADR 0036 step 3: a recorded-as-of read returns trust as of T.
//
// On Monday the deploy belief rests on one episode, and a deploy log from
// another author is ingested but not yet linked. On Thursday the log is linked
// to the belief (a second, independent source) and a validated outcome credits
// it. Today's trust reflects both. Tuesday's must not: the link and the credit
// were recorded after Tuesday, even though the log itself was ingested before.
func TestRecordedAsOf_TrustIsReconstructedAtT(t *testing.T) {
	s := openTimeline(t)
	ctx := s.ctx

	// Monday.
	s.add("ev-deploy", "cl-deploy", "payments outage context: the v2 deploy changed the payments connection pool")
	monday := time.Now().UTC()
	if err := s.conn.Events.Append(ctx, domain.Event{
		ID: "ev-deploy-log", RunID: "timeline", SchemaVersion: "v1", SourceInputID: "deploy-log",
		Content: "deploy log: v2 rolled out, connection pool settings changed", CreatedBy: "ci",
		Timestamp: monday, IngestedAt: monday,
	}); err != nil {
		t.Fatalf("append deploy log: %v", err)
	}
	tuesday := tick()

	// Thursday: the log is linked and the belief is credited.
	if err := s.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: "cl-deploy", EventID: "ev-deploy-log"}}); err != nil {
		t.Fatalf("link deploy log: %v", err)
	}
	writer, ok := s.conn.Claims.(ports.BeliefCreditWriter)
	if !ok {
		t.Fatalf("sqlite claims do not implement BeliefCreditWriter")
	}
	if err := writer.ApplyBeliefCredit(ctx, "cl-deploy", map[string]float64{domain.CreditAppliedComponentKey: 0.1}, 0.8); err != nil {
		t.Fatalf("credit: %v", err)
	}
	// Thursday also re-extracts the belief with more confidence, verifies it
	// with a longer half-life and confirms it (ADR 0036 §7).
	held, err := s.conn.Claims.ListByIDs(ctx, []string{"cl-deploy"})
	if err != nil || len(held) != 1 {
		t.Fatalf("reload cl-deploy before restating: %v (%d)", err, len(held))
	}
	restated := held[0]
	restated.Confidence = 0.95
	if err := s.conn.Claims.Upsert(ctx, []domain.Claim{restated}); err != nil {
		t.Fatalf("restate with more confidence: %v", err)
	}
	if err := s.conn.Claims.MarkVerified(ctx, "cl-deploy", time.Time{}, 365); err != nil {
		t.Fatalf("verify with a half-life: %v", err)
	}
	if err := s.conn.Claims.MarkConfirmed(ctx, "cl-deploy", time.Now().UTC()); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	scorer, ok := s.conn.Claims.(ports.ScopedTrustScorer)
	if !ok {
		t.Fatalf("sqlite claims do not implement ScopedTrustScorer")
	}
	if _, err := scorer.RecomputeTrustForClaims(ctx, []string{"cl-deploy"}, trust.Scorer(time.Now().UTC())); err != nil {
		t.Fatalf("recompute trust: %v", err)
	}
	tick()

	stored, err := s.conn.Claims.ListByIDs(ctx, []string{"cl-deploy"})
	if err != nil || len(stored) != 1 {
		t.Fatalf("reload cl-deploy: %v (%d)", err, len(stored))
	}
	today := stored[0].TrustScore

	engine := NewEngine(s.conn.Events.(eventLister), s.conn.Claims, s.conn.Relationships)
	const question = "payments outage context v2 deploy connection pool"
	trustOf := func(ans domain.Answer) (float64, bool) {
		for _, c := range ans.Claims {
			if c.ID == "cl-deploy" {
				return c.TrustScore, true
			}
		}
		return 0, false
	}

	now, err := engine.AnswerWithOptions(ctx, question, AnswerOptions{})
	if err != nil {
		t.Fatalf("answer now: %v", err)
	}
	if got, ok := trustOf(now); !ok || got != today {
		t.Errorf("today's read returns trust %v (found=%v), want the stored %v", got, ok, today)
	}

	then, err := engine.AnswerWithOptions(ctx, question, AnswerOptions{RecordedAsOf: tuesday})
	if err != nil {
		t.Fatalf("answer as of Tuesday: %v", err)
	}
	got, ok := trustOf(then)
	if !ok {
		t.Fatalf("Tuesday's answer is missing cl-deploy")
	}
	want := trust.At(domain.TrustInput{
		Confidence: 0.8, EvidenceCount: 1, LatestEvidence: stored[0].CreatedAt,
	}, tuesday)
	if math.Abs(got-want) > 1e-3 {
		t.Errorf("trust at Tuesday = %.4f, want %.4f (one source, no credit, Monday's confidence and half-life, unconfirmed); today's is %.4f", got, want, today)
	}
	if math.Abs(got-today) < 1e-3 {
		t.Errorf("trust at Tuesday equals today's %.4f: Thursday's evidence, credit, confidence, half-life or confirmation leaked into the past", today)
	}
	for _, c := range then.Claims {
		if c.ID == "cl-deploy" && (c.Confidence != 0.8 || c.HalfLifeDays != 0 || !c.LastConfirmed.IsZero()) {
			t.Errorf("Tuesday's cl-deploy reads confidence %v, half-life %v, confirmed %v; want Monday's 0.8, 0, never",
				c.Confidence, c.HalfLifeDays, c.LastConfirmed)
		}
	}
}

func recordedRelationships(rels ports.RelationshipRepository, claims ports.ClaimRepository, at time.Time) recorded.Relationships {
	return recorded.Relationships{RelationshipRepository: rels, Claims: claims, At: at}
}

// ADR 0036 §1: a recorded-as-of read asks the validity filter its question at
// T, not at the wall clock. In February the brain records that search moves
// from Discovery to Platform in March: Discovery's valid_to is set to a March
// instant and Platform's valid_from is that instant. Asked "who owns search"
// as recorded in February, the brain then answered Discovery — still in force
// at T — and not Platform, not yet in force. Reading validity at today's clock
// instead drops Discovery (its valid_to has since passed) and admits Platform,
// an answer the brain never gave.
func TestRecordedAsOf_ValidityIsReadAtT(t *testing.T) {
	s := openTimeline(t)
	s.add("ev-discovery", "cl-discovery", "the search service is owned by the Discovery team")
	time.Sleep(5 * time.Millisecond)

	// February: the handover is announced, effective at a later instant.
	effective := time.Now().UTC().Add(150 * time.Millisecond)
	now := time.Now().UTC()
	if err := s.conn.Events.Append(s.ctx, domain.Event{
		ID: "ev-platform", RunID: "timeline", SchemaVersion: "v1", SourceInputID: "timeline",
		Content: "the search service is owned by the Platform team", Timestamp: effective, IngestedAt: now,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.conn.Claims.Upsert(s.ctx, []domain.Claim{{
		ID: "cl-platform", Text: "the search service is owned by the Platform team", Type: domain.ClaimTypeFact,
		Status: domain.ClaimStatusActive, Confidence: 0.8, TrustScore: 0.8, CreatedAt: now, ValidFrom: effective,
	}}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.conn.Claims.UpsertEvidence(s.ctx, []domain.ClaimEvidence{{ClaimID: "cl-platform", EventID: "ev-platform"}}); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if err := s.conn.Claims.SetValidity(s.ctx, "cl-discovery", effective, domain.ChangeNote{Reason: "announced handover"}); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	february := tick()
	time.Sleep(time.Until(effective) + 5*time.Millisecond)

	engine := NewEngine(s.conn.Events.(eventLister), s.conn.Claims, s.conn.Relationships)
	ans, err := engine.AnswerWithOptions(s.ctx, "who owns the search service", AnswerOptions{RecordedAsOf: february})
	if err != nil {
		t.Fatalf("answer as of February: %v", err)
	}
	got := map[string]bool{}
	for _, c := range ans.Claims {
		got[c.ID] = true
	}
	if !got["cl-discovery"] {
		t.Errorf("February's answer is missing Discovery, in force in February")
	}
	if got["cl-platform"] {
		t.Errorf("February's answer contains Platform, not in force until March")
	}

	// Today the handover has happened.
	ans, err = engine.AnswerWithOptions(s.ctx, "who owns the search service", AnswerOptions{})
	if err != nil {
		t.Fatalf("answer now: %v", err)
	}
	got = map[string]bool{}
	for _, c := range ans.Claims {
		got[c.ID] = true
	}
	if !got["cl-platform"] || got["cl-discovery"] {
		t.Errorf("today's answer = %v, want Platform only", got)
	}
}
