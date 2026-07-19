package govwrite_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// newWriter opens a fresh in-memory store and wraps it in a governed
// Writer. The Writer owns the conn so the test only defers Close.
func newWriter(t *testing.T) *govwrite.Writer {
	t.Helper()
	w, err := govwrite.New(context.Background(), "memory://", nil)
	if err != nil {
		t.Fatalf("govwrite.New: %v", err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return w
}

// assertEvidence fails unless the last governed write left a verifiable
// evidence chain with at least one record of the expected kind tagged
// with the daemon source. This is the load-bearing assertion: it proves
// the write routed THROUGH the kernel rather than around it.
func assertEvidence(t *testing.T, w *govwrite.Writer, wantKind string) {
	t.Helper()
	session := w.LastSession()
	if session == nil {
		t.Fatal("no kernel session recorded — write bypassed the kernel")
	}
	if err := session.VerifyEvidenceChain(); err != nil {
		t.Fatalf("evidence chain broken: %v", err)
	}
	for _, rec := range session.Evidence() {
		if rec.Kind == wantKind {
			if rec.Source != govwrite.Source {
				t.Errorf("evidence %s source = %q, want %q", wantKind, rec.Source, govwrite.Source)
			}
			return
		}
	}
	t.Fatalf("no evidence record of kind %q on the session", wantKind)
}

func TestWriter_Events_PreservesCustomIDAndStructuredFields(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()

	evt := domain.Event{
		ID:            "ev_git_0123456789abcdef",
		RunID:         "git-log-x",
		SchemaVersion: "git-log/1.0",
		Content:       "fix: thing",
		SourceInputID: "git_0123456789abcdef",
		Timestamp:     time.Now().UTC(),
		Metadata:      map[string]string{"source": "git", "git_commit_sha": "0123456789abcdef"},
		IngestedAt:    time.Now().UTC(),
		CreatedBy:     "tester",
	}
	n, err := w.Events(ctx, []domain.Event{evt})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if n != 1 {
		t.Fatalf("accepted = %d, want 1", n)
	}
	assertEvidence(t, w, "mnemos.write.events")

	got, err := w.Conn().Events.GetByID(ctx, evt.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != evt.ID || got.SchemaVersion != evt.SchemaVersion || got.SourceInputID != evt.SourceInputID {
		t.Errorf("structured fields lost: got id=%q schema=%q source_input=%q",
			got.ID, got.SchemaVersion, got.SourceInputID)
	}
}

func TestWriter_Claims_WithReason(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()

	claim := domain.Claim{
		ID:         "cl_1",
		Text:       "the sky is blue",
		Type:       domain.ClaimTypeFact,
		Confidence: 1.0,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  time.Now().UTC(),
		CreatedBy:  "tester",
	}
	n, err := w.Claims(ctx, []domain.Claim{claim}, govwrite.ClaimReason{Reason: "agent-remember", ChangedBy: "agent-7"})
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if n != 1 {
		t.Fatalf("accepted = %d, want 1", n)
	}
	assertEvidence(t, w, "mnemos.write.claims")

	got, err := w.Conn().Claims.ListByIDs(ctx, []string{"cl_1"})
	if err != nil || len(got) != 1 {
		t.Fatalf("ListByIDs: got %d (err=%v)", len(got), err)
	}
}

func TestWriter_EvidenceLinks(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	// A claim and event must exist for the link to be meaningful, but the
	// link table is dumb — assert the governed write routes + records.
	n, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{{ClaimID: "cl_1", EventID: "ev_1"}})
	if err != nil {
		t.Fatalf("EvidenceLinks: %v", err)
	}
	if n != 1 {
		t.Fatalf("accepted = %d, want 1", n)
	}
	assertEvidence(t, w, "mnemos.write.evidence_links")
}

func TestWriter_Relationships(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	rel := domain.Relationship{
		ID:          "rel_1",
		Type:        domain.RelationshipTypeSupports,
		FromClaimID: "cl_a",
		ToClaimID:   "cl_b",
		CreatedAt:   time.Now().UTC(),
		CreatedBy:   "tester",
	}
	n, err := w.Relationships(ctx, []domain.Relationship{rel})
	if err != nil {
		t.Fatalf("Relationships: %v", err)
	}
	if n != 1 {
		t.Fatalf("accepted = %d, want 1", n)
	}
	assertEvidence(t, w, "mnemos.write.relationships")
}

func TestWriter_Embedding(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	if err := w.Embedding(ctx, "cl_1", "claim", []float32{0.1, 0.2, 0.3}, "test-model", "tester"); err != nil {
		t.Fatalf("Embedding: %v", err)
	}
	assertEvidence(t, w, "mnemos.write.embedding")
	recs, err := w.Conn().Embeddings.ListByEntityType(ctx, "claim")
	if err != nil || len(recs) != 1 {
		t.Fatalf("ListByEntityType: got %d (err=%v)", len(recs), err)
	}
}

func TestWriter_Action(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	a := domain.Action{
		ID:        "ac_1",
		Kind:      domain.ActionKind("deploy"),
		Subject:   "api",
		Actor:     "tester",
		At:        time.Now().UTC(),
		CreatedBy: "tester",
	}
	id, err := w.Action(ctx, a)
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if id != "ac_1" {
		t.Fatalf("id = %q, want ac_1", id)
	}
	assertEvidence(t, w, "mnemos.write.action")
	got, err := w.Conn().Actions.GetByID(ctx, "ac_1")
	if err != nil || got.ID != "ac_1" {
		t.Fatalf("GetByID: %v (%q)", err, got.ID)
	}
}

func TestWriter_Outcome_WithAutoEdge(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	// Seed the action so the outcome's auto-edge has a real parent.
	if _, err := w.Action(ctx, domain.Action{
		ID: "ac_1", Kind: domain.ActionKind("deploy"), Subject: "api", At: time.Now().UTC(), CreatedBy: "t",
	}); err != nil {
		t.Fatalf("seed action: %v", err)
	}
	o := domain.Outcome{
		ID:         "oc_1",
		ActionID:   "ac_1",
		Result:     domain.OutcomeResultSuccess,
		ObservedAt: time.Now().UTC(),
		Source:     "push",
		CreatedBy:  "tester",
	}
	id, err := w.Outcome(ctx, o, true)
	if err != nil {
		t.Fatalf("Outcome: %v", err)
	}
	if id != "oc_1" {
		t.Fatalf("id = %q, want oc_1", id)
	}
	assertEvidence(t, w, "mnemos.write.outcome")
	// The auto-edge wiring should have produced entity edges.
	edges, err := w.Conn().EntityRels.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll edges: %v", err)
	}
	if len(edges) == 0 {
		t.Error("expected auto-linked action_of/outcome_of edges, got none")
	}
}

func TestWriter_Lesson(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	l := domain.Lesson{
		ID:         "ls_1",
		Statement:  "roll back fast",
		Confidence: 0.6,
		Evidence:   []string{"human"},
		DerivedAt:  time.Now().UTC(),
		Source:     "human",
		CreatedBy:  "tester",
	}
	id, err := w.Lesson(ctx, l)
	if err != nil {
		t.Fatalf("Lesson: %v", err)
	}
	if id != "ls_1" {
		t.Fatalf("id = %q", id)
	}
	assertEvidence(t, w, "mnemos.write.lesson")
}

func TestWriter_Decision(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	d := domain.Decision{
		ID:        "dc_1",
		Statement: "ship it",
		RiskLevel: domain.RiskLevelMedium,
		ChosenAt:  time.Now().UTC(),
		CreatedBy: "tester",
	}
	id, err := w.Decision(ctx, d)
	if err != nil {
		t.Fatalf("Decision: %v", err)
	}
	if id != "dc_1" {
		t.Fatalf("id = %q", id)
	}
	assertEvidence(t, w, "mnemos.write.decision")
}

func TestWriter_Playbook(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	p := domain.Playbook{
		ID:         "pb_1",
		Trigger:    "latency_spike",
		Statement:  "when X do Y",
		Confidence: 0.6,
		DerivedAt:  time.Now().UTC(),
		Source:     "human",
		CreatedBy:  "tester",
	}
	id, err := w.Playbook(ctx, p)
	if err != nil {
		t.Fatalf("Playbook: %v", err)
	}
	if id != "pb_1" {
		t.Fatalf("id = %q", id)
	}
	assertEvidence(t, w, "mnemos.write.playbook")
}

func TestWriter_EntityRels(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	edge := domain.EntityRelationship{
		ID:        "er_1",
		Kind:      domain.RelationshipTypeActionOf,
		FromID:    "ac_1",
		FromType:  domain.RelEntityAction,
		ToID:      "oc_1",
		ToType:    domain.RelEntityOutcome,
		CreatedAt: time.Now().UTC(),
		CreatedBy: "tester",
	}
	n, err := w.EntityRels(ctx, []domain.EntityRelationship{edge})
	if err != nil {
		t.Fatalf("EntityRels: %v", err)
	}
	if n != 1 {
		t.Fatalf("accepted = %d", n)
	}
	assertEvidence(t, w, "mnemos.write.entity_rels")
}

func TestWriter_Incident_AndResolve(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	inc := domain.Incident{
		ID:        "inc_1",
		Title:     "outage",
		Severity:  domain.IncidentSeverityHigh,
		Status:    domain.IncidentStatusOpen,
		OpenedAt:  time.Now().UTC(),
		CreatedBy: "tester",
	}
	id, err := w.Incident(ctx, inc)
	if err != nil {
		t.Fatalf("Incident: %v", err)
	}
	if id != "inc_1" {
		t.Fatalf("id = %q", id)
	}
	assertEvidence(t, w, "mnemos.write.incident")

	if err := w.ResolveIncident(ctx, "inc_1", time.Now().UTC()); err != nil {
		t.Fatalf("ResolveIncident: %v", err)
	}
	assertEvidence(t, w, "mnemos.write.resolve_incident")
	got, found, err := w.Conn().Incidents.GetByID(ctx, "inc_1")
	if err != nil || !found {
		t.Fatalf("GetByID: %v found=%v", err, found)
	}
	if got.Status != domain.IncidentStatusResolved {
		t.Errorf("status = %q, want resolved", got.Status)
	}
}

func TestWriter_Feedback(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	state := domain.ClaimFeedback{
		ClaimID:        "cl_1",
		HelpfulCount:   3,
		LastFeedbackAt: time.Now().UTC(),
	}
	id, err := w.Feedback(ctx, state)
	if err != nil {
		t.Fatalf("Feedback: %v", err)
	}
	if id != "cl_1" {
		t.Fatalf("id = %q", id)
	}
	assertEvidence(t, w, "mnemos.write.feedback")
}

func TestWriter_Artifacts(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	evt := domain.Event{
		ID:            "ev_1",
		Content:       "deployed api v2",
		SchemaVersion: "1.0",
		SourceInputID: "inline:ev_1",
		Timestamp:     time.Now().UTC(),
		IngestedAt:    time.Now().UTC(),
		CreatedBy:     "tester",
	}
	claim := domain.Claim{
		ID:         "cl_1",
		Text:       "api v2 deployed",
		Type:       domain.ClaimTypeFact,
		Confidence: 0.9,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  time.Now().UTC(),
		CreatedBy:  "tester",
	}
	link := domain.ClaimEvidence{ClaimID: "cl_1", EventID: "ev_1"}

	n, err := w.Artifacts(ctx, []domain.Event{evt}, []domain.Claim{claim}, []domain.ClaimEvidence{link}, nil)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if n != 1 {
		t.Fatalf("events accepted = %d, want 1", n)
	}
	assertEvidence(t, w, "mnemos.write.artifacts")
	if _, err := w.Conn().Events.GetByID(ctx, "ev_1"); err != nil {
		t.Fatalf("event not persisted: %v", err)
	}
	got, err := w.Conn().Claims.ListByIDs(ctx, []string{"cl_1"})
	if err != nil || len(got) != 1 {
		t.Fatalf("claim not persisted: %v (%d)", err, len(got))
	}
}

func TestWriter_AttachOutcome(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	// Seed action, outcome, decision so attach + autoedge has real rows.
	if _, err := w.Action(ctx, domain.Action{ID: "ac_1", Kind: "deploy", Subject: "api", At: time.Now().UTC(), CreatedBy: "t"}); err != nil {
		t.Fatalf("seed action: %v", err)
	}
	if _, err := w.Outcome(ctx, domain.Outcome{ID: "oc_1", ActionID: "ac_1", Result: domain.OutcomeResultSuccess, ObservedAt: time.Now().UTC(), Source: "push", CreatedBy: "t"}, false); err != nil {
		t.Fatalf("seed outcome: %v", err)
	}
	if _, err := w.Decision(ctx, domain.Decision{ID: "dc_1", Statement: "ship", RiskLevel: domain.RiskLevelMedium, Beliefs: []string{"cl_belief"}, ChosenAt: time.Now().UTC(), CreatedBy: "t"}); err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	if err := w.AttachOutcome(ctx, "dc_1", "oc_1", "tester"); err != nil {
		t.Fatalf("AttachOutcome: %v", err)
	}
	assertEvidence(t, w, "mnemos.write.attach_outcome")
}

func TestWriter_Wrap_BorrowsConn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn, err := store.Open(ctx, "memory://")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	w, err := govwrite.Wrap(conn, nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	// Close must NOT close a borrowed conn — a subsequent read still works.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := conn.Events.ListAll(ctx); err != nil {
		t.Errorf("borrowed conn closed by Writer.Close: %v", err)
	}
}

func TestWriter_NilConn(t *testing.T) {
	t.Parallel()
	if _, err := govwrite.Wrap(nil, nil); err == nil {
		t.Fatal("Wrap(nil) should error")
	}
}

func TestWriter_PruneRelationships(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()

	seed := []domain.Relationship{
		{ID: "rel_keep", Type: domain.RelationshipTypeSupports, FromClaimID: "cl_a", ToClaimID: "cl_b", CreatedAt: time.Now().UTC(), CreatedBy: "tester"},
		{ID: "rel_drop", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl_c", ToClaimID: "cl_d", CreatedAt: time.Now().UTC(), CreatedBy: "tester"},
	}
	if _, err := w.Relationships(ctx, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Keep only the first; the executor replaces the whole set.
	n, err := w.PruneRelationships(ctx, seed[:1], 1)
	if err != nil {
		t.Fatalf("PruneRelationships: %v", err)
	}
	if n != 1 {
		t.Fatalf("retained = %d, want 1", n)
	}
	// Dropping edges is a real mutation and must leave an audit trail.
	assertEvidence(t, w, "mnemos.write.relationships.prune")

	got, err := w.Conn().Relationships.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(got) != 1 || got[0].ID != "rel_keep" {
		t.Fatalf("after prune got %d edges (%v), want just rel_keep", len(got), got)
	}
}

// Pruning to nothing must be allowed — a brain whose every edge is stale is a
// legitimate state — and must not error on the empty Upsert.
func TestWriter_PruneRelationships_ToEmpty(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()

	if _, err := w.Relationships(ctx, []domain.Relationship{
		{ID: "rel_1", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl_a", ToClaimID: "cl_b", CreatedAt: time.Now().UTC(), CreatedBy: "tester"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	n, err := w.PruneRelationships(ctx, nil, 1)
	if err != nil {
		t.Fatalf("PruneRelationships: %v", err)
	}
	if n != 0 {
		t.Errorf("retained = %d, want 0", n)
	}
	got, err := w.Conn().Relationships.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d edges after pruning everything, want 0", len(got))
	}
}
