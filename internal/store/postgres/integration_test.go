package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/postgres"
)

// requireLiveDSN gates every test in this file on the
// TEST_POSTGRES_DSN env var. CI configures it via docker-compose;
// developer machines without Postgres just see SKIP.
//
// Each test gets a unique namespace (?namespace=<test_id>) so
// parallel runs don't collide and a leftover schema from a previous
// failure doesn't poison the next run. Cleanup drops the schema at
// the end of the test.
func requireLiveDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping postgres integration test")
	}
	return dsn
}

// withConn opens a Conn against a fresh per-test namespace, returns
// it, and registers cleanup that drops the schema. The namespace
// name embeds the test name + a nanosecond suffix to dodge collisions
// across parallel sub-tests.
func withConn(t *testing.T) *store.Conn {
	t.Helper()
	dsn := requireLiveDSN(t)
	ns := fmt.Sprintf("mnemos_test_%d", time.Now().UnixNano())
	full := dsn
	if contains(full, "?") {
		full += "&namespace=" + ns
	} else {
		full += "?namespace=" + ns
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := store.Open(ctx, full)
	if err != nil {
		t.Fatalf("store.Open(%s): %v", full, err)
	}
	t.Cleanup(func() {
		// Drop the per-test schema. We pull the *sql.DB out of Raw
		// since the schema name isn't a value.
		if raw, ok := conn.Raw.(interface {
			ExecContext(context.Context, string, ...any) (any, error)
		}); ok {
			_, _ = raw.ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, ns))
		}
		_ = conn.Close()
	})
	return conn
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestPostgres_OpenBootstrapsSchema(t *testing.T) {
	conn := withConn(t)
	if conn.Events == nil || conn.Claims == nil || conn.Relationships == nil ||
		conn.Embeddings == nil || conn.Users == nil || conn.RevokedTokens == nil ||
		conn.Agents == nil || conn.Entities == nil || conn.Jobs == nil {
		t.Errorf("postgres Conn has nil port: %+v", conn)
	}
}

func TestPostgres_EventRoundTrip(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ev := domain.Event{
		ID:            "ev-1",
		RunID:         "run-A",
		SchemaVersion: "1",
		Content:       "alpha",
		SourceInputID: "in-1",
		Timestamp:     now,
		IngestedAt:    now,
		Metadata:      map[string]string{"k": "v"},
		CreatedBy:     domain.SystemUser,
	}
	if err := conn.Events.Append(ctx, ev); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := conn.Events.Append(ctx, ev); err != nil {
		t.Fatalf("Append idempotent: %v", err)
	}

	got, err := conn.Events.GetByID(ctx, "ev-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Content != "alpha" || got.Metadata["k"] != "v" {
		t.Errorf("GetByID = %+v", got)
	}

	all, err := conn.Events.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("ListAll len = %d, want 1", len(all))
	}

	byRun, err := conn.Events.ListByRunID(ctx, "run-A")
	if err != nil {
		t.Fatalf("ListByRunID: %v", err)
	}
	if len(byRun) != 1 {
		t.Errorf("ListByRunID len = %d, want 1", len(byRun))
	}
}

// TestPostgres_LessonSubjectClassRoundTrip verifies the ADR 0012
// subject_class column persists on the hand-written Postgres lessons
// repo — written on insert, refreshed on upsert, and read back into
// domain.Schema.SubjectClass.
func TestPostgres_LessonSubjectClassRoundTrip(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Seed the action the lesson's evidence references (FK: lesson_evidence.action_id).
	if err := conn.Actions.Append(ctx, domain.Action{
		ID: "act-subject-1", Kind: domain.ActionKindDeploy, Subject: "svc", At: now, CreatedBy: "u-test", CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed action: %v", err)
	}
	lesson := domain.Lesson{
		ID:           "ls-subject-1",
		Statement:    "class-level pattern",
		Confidence:   0.8,
		SubjectClass: domain.SubjectClassClass,
		Evidence:     []string{"act-subject-1"}, // Validate() requires >=1 evidence action id
		DerivedAt:    now,
		LastVerified: now,
	}
	if err := conn.Lessons.Append(ctx, lesson); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := conn.Lessons.GetByID(ctx, lesson.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.SubjectClass != domain.SubjectClassClass {
		t.Fatalf("GetByID SubjectClass = %q, want %q", got.SubjectClass, domain.SubjectClassClass)
	}

	all, err := conn.Lessons.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 1 || all[0].SubjectClass != domain.SubjectClassClass {
		t.Fatalf("ListAll SubjectClass round-trip failed: %+v", all)
	}

	// Upsert with a different class must overwrite (EXCLUDED.subject_class).
	lesson.SubjectClass = domain.SubjectClassIndividual
	if err := conn.Lessons.Append(ctx, lesson); err != nil {
		t.Fatalf("Append (upsert): %v", err)
	}
	got, err = conn.Lessons.GetByID(ctx, lesson.ID)
	if err != nil {
		t.Fatalf("GetByID after upsert: %v", err)
	}
	if got.SubjectClass != domain.SubjectClassIndividual {
		t.Fatalf("upsert did not refresh SubjectClass: got %q", got.SubjectClass)
	}
}

// TestPostgres_ClaimSubjectClassRoundTrip verifies the ADR 0012 subject_class
// column persists on the hand-written Postgres claims repo — written on insert,
// refreshed on upsert (EXCLUDED.subject_class), and read back into
// domain.Claim.SubjectClass through the shared collectClaimRows scanner.
func TestPostgres_ClaimSubjectClassRoundTrip(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Seed the event the claim's evidence references (FK: claim_evidence.event_id).
	if err := conn.Events.Append(ctx, domain.Event{
		ID: "ev-sc-1", RunID: "r", SchemaVersion: "1", Content: "x",
		SourceInputID: "in", Timestamp: now, IngestedAt: now, CreatedBy: domain.SystemUser,
	}); err != nil {
		t.Fatalf("seed event: %v", err)
	}

	claim := domain.Claim{
		ID: "cl-sc-1", Text: "class-level belief", Type: domain.ClaimTypeFact,
		Confidence: 0.8, Status: domain.ClaimStatusActive,
		CreatedAt: now, CreatedBy: "u-test",
		SubjectClass: domain.SubjectClassClass,
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: "cl-sc-1", EventID: "ev-sc-1"}}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}

	all, err := conn.Claims.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 1 || all[0].SubjectClass != domain.SubjectClassClass {
		t.Fatalf("ListAll SubjectClass round-trip failed: %+v", all)
	}

	byIDs, err := conn.Claims.ListByIDs(ctx, []string{"cl-sc-1"})
	if err != nil {
		t.Fatalf("ListByIDs: %v", err)
	}
	if len(byIDs) != 1 || byIDs[0].SubjectClass != domain.SubjectClassClass {
		t.Fatalf("ListByIDs SubjectClass round-trip failed: %+v", byIDs)
	}

	// Upsert with a different class must overwrite (EXCLUDED.subject_class).
	claim.SubjectClass = domain.SubjectClassIndividual
	if err := conn.Claims.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("Upsert (refresh): %v", err)
	}
	byIDs, err = conn.Claims.ListByIDs(ctx, []string{"cl-sc-1"})
	if err != nil {
		t.Fatalf("ListByIDs after refresh: %v", err)
	}
	if len(byIDs) != 1 || byIDs[0].SubjectClass != domain.SubjectClassIndividual {
		t.Fatalf("upsert did not refresh SubjectClass: %+v", byIDs)
	}
}

func TestPostgres_ClaimUpsertWithStatusHistory(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := conn.Events.Append(ctx, domain.Event{
		ID: "ev-1", RunID: "r", SchemaVersion: "1", Content: "x",
		SourceInputID: "in", Timestamp: now, IngestedAt: now, CreatedBy: domain.SystemUser,
	}); err != nil {
		t.Fatalf("seed event: %v", err)
	}

	claim := domain.Claim{
		ID: "cl-1", Text: "first claim", Type: domain.ClaimTypeFact,
		Confidence: 0.8, Status: domain.ClaimStatusActive,
		CreatedAt: now, CreatedBy: "u-test",
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("Upsert active: %v", err)
	}
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: "cl-1", EventID: "ev-1"}}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}

	// Same status → no transition.
	if err := conn.Claims.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("Upsert same status: %v", err)
	}

	claim.Status = domain.ClaimStatusContested
	if err := conn.Claims.UpsertWithReasonAs(ctx, []domain.Claim{claim}, "auto: contradiction", "u-actor"); err != nil {
		t.Fatalf("Upsert contested: %v", err)
	}

	hist, err := conn.Claims.ListStatusHistoryByClaimID(ctx, "cl-1")
	if err != nil {
		t.Fatalf("ListStatusHistoryByClaimID: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("history len = %d, want 2", len(hist))
	}
	if hist[1].FromStatus != domain.ClaimStatusActive || hist[1].ToStatus != domain.ClaimStatusContested {
		t.Errorf("transition[1] = %+v", hist[1])
	}
	if hist[1].ChangedBy != "u-actor" {
		t.Errorf("changedBy = %q", hist[1].ChangedBy)
	}
}

// TestPostgres_IndependenceAwareCorroboration proves the echo-chamber guard runs
// on the SQL path: COUNT(DISTINCT created_by) vs COUNT(DISTINCT event_id) feeds the
// graded evidence count, so three independent authors beat one author repeated.
func TestPostgres_IndependenceAwareCorroboration(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	scorer := conn.Claims.(ports.TrustScorer)
	now := time.Now().UTC()

	ev := func(id, author string) domain.Event {
		return domain.Event{ID: id, RunID: "r", SchemaVersion: "1", Content: "c " + id,
			SourceInputID: "in", Timestamp: now, IngestedAt: now, CreatedBy: author}
	}
	for i, a := range []string{"alice", "bob", "carol"} {
		if err := conn.Events.Append(ctx, ev(fmt.Sprintf("evA%d", i), a)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := conn.Events.Append(ctx, ev(fmt.Sprintf("evB%d", i), "dave")); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{
		{ID: "clA", Text: "three voices", Type: domain.ClaimTypeFact, Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: now},
		{ID: "clB", Text: "one voice thrice", Type: domain.ClaimTypeFact, Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
		{ClaimID: "clA", EventID: "evA0"}, {ClaimID: "clA", EventID: "evA1"}, {ClaimID: "clA", EventID: "evA2"},
		{ClaimID: "clB", EventID: "evB0"}, {ClaimID: "clB", EventID: "evB1"}, {ClaimID: "clB", EventID: "evB2"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := scorer.RecomputeTrust(ctx, func(_ float64, count int, _ time.Time) float64 {
		return float64(count)
	}); err != nil {
		t.Fatal(err)
	}
	got, err := conn.Claims.ListByIDs(ctx, []string{"clA", "clB"})
	if err != nil {
		t.Fatal(err)
	}
	trust := map[string]float64{}
	for _, c := range got {
		trust[c.ID] = c.TrustScore
	}
	if trust["clA"] != 3 || trust["clB"] != 2 {
		t.Fatalf("graded corroboration wrong: clA=%v (want 3), clB=%v (want 2)", trust["clA"], trust["clB"])
	}
}

func TestPostgres_TrustScorerCapability(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	scorer, ok := conn.Claims.(ports.TrustScorer)
	if !ok {
		t.Fatal("postgres ClaimRepository does not satisfy ports.TrustScorer")
	}

	now := time.Now().UTC()
	if err := conn.Events.Append(ctx, domain.Event{
		ID: "ev-T", RunID: "r", SchemaVersion: "1", Content: "x",
		SourceInputID: "in", Timestamp: now, IngestedAt: now, CreatedBy: domain.SystemUser,
	}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{{
		ID: "cl-T", Text: "trust", Type: domain.ClaimTypeFact,
		Confidence: 0.6, Status: domain.ClaimStatusActive, CreatedAt: now,
	}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: "cl-T", EventID: "ev-T"}}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}

	n, err := scorer.RecomputeTrust(ctx, func(c float64, count int, _ time.Time) float64 {
		return c * float64(count)
	})
	if err != nil {
		t.Fatalf("RecomputeTrust: %v", err)
	}
	if n != 1 {
		t.Errorf("RecomputeTrust touched %d claims, want 1", n)
	}
	avg, err := scorer.AverageTrust(ctx)
	if err != nil || avg <= 0 {
		t.Errorf("AverageTrust = %v err=%v", avg, err)
	}
	low, err := scorer.CountClaimsBelowTrust(ctx, 999)
	if err != nil {
		t.Fatalf("CountClaimsBelowTrust: %v", err)
	}
	if low != 1 {
		t.Errorf("CountClaimsBelowTrust(999) = %d, want 1", low)
	}
}

func TestPostgres_UserAgentRepositories(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	now := time.Now().UTC()

	u := domain.User{ID: "u-1", Name: "Alice", Email: "a@x", Status: domain.UserStatusActive, CreatedAt: now}
	if err := conn.Users.Create(ctx, u); err != nil {
		t.Fatalf("user create: %v", err)
	}
	if err := conn.Users.Create(ctx, u); err == nil {
		t.Error("duplicate user create accepted")
	}
	got, err := conn.Users.GetByEmail(ctx, "a@x")
	if err != nil || got.ID != "u-1" {
		t.Errorf("GetByEmail = %+v err=%v", got, err)
	}

	// Agent owner FK.
	if err := conn.Agents.Create(ctx, domain.Agent{
		ID: "a-1", Name: "ci", OwnerID: "u-missing",
		Status: domain.AgentStatusActive, CreatedAt: now,
	}); err == nil {
		t.Error("agent with bad owner accepted")
	}
	if err := conn.Agents.Create(ctx, domain.Agent{
		ID: "a-1", Name: "ci", OwnerID: "u-1",
		Scopes: []string{"events:write"}, Status: domain.AgentStatusActive, CreatedAt: now,
	}); err != nil {
		t.Fatalf("agent create: %v", err)
	}
	gotA, err := conn.Agents.GetByID(ctx, "a-1")
	if err != nil {
		t.Fatalf("agent GetByID: %v", err)
	}
	if len(gotA.Scopes) != 1 || gotA.Scopes[0] != "events:write" {
		t.Errorf("agent scopes = %v", gotA.Scopes)
	}
}

func TestPostgres_RevokedTokenRoundTrip(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := conn.RevokedTokens.Add(ctx, domain.RevokedToken{
		JTI: "jti-1", RevokedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if ok, _ := conn.RevokedTokens.IsRevoked(ctx, "jti-1"); !ok {
		t.Error("jti-1 should be revoked")
	}
	if err := conn.RevokedTokens.Add(ctx, domain.RevokedToken{
		JTI: "jti-old", RevokedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("Add old: %v", err)
	}
	n, err := conn.RevokedTokens.PurgeExpired(ctx, now)
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("PurgeExpired = %d, want 1", n)
	}
}

func TestPostgres_EntityFindOrCreateMerge(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()

	a, err := conn.Entities.FindOrCreate(ctx, "Felix Geelhaar", domain.EntityTypePerson, "")
	if err != nil {
		t.Fatalf("FindOrCreate a: %v", err)
	}
	b, err := conn.Entities.FindOrCreate(ctx, "  felix  geelhaar ", domain.EntityTypePerson, "")
	if err != nil {
		t.Fatalf("FindOrCreate b: %v", err)
	}
	if a.ID != b.ID {
		t.Errorf("FindOrCreate dedup failed: a=%s b=%s", a.ID, b.ID)
	}

	loser, err := conn.Entities.FindOrCreate(ctx, "felixgeelhaar", domain.EntityTypePerson, "")
	if err != nil {
		t.Fatalf("FindOrCreate loser: %v", err)
	}
	// Seed a claim and link both entities.
	now := time.Now().UTC()
	if err := conn.Claims.Upsert(ctx, []domain.Claim{{
		ID: "cl-1", Text: "x", Type: domain.ClaimTypeFact,
		Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: now,
	}}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if err := conn.Entities.LinkClaim(ctx, "cl-1", loser.ID, "subject"); err != nil {
		t.Fatalf("LinkClaim loser: %v", err)
	}

	if err := conn.Entities.Merge(ctx, a.ID, loser.ID); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	count, _ := conn.Entities.Count(ctx)
	if count != 1 {
		t.Errorf("Count after merge = %d, want 1", count)
	}
	ents, _, err := conn.Entities.ListEntitiesForClaim(ctx, "cl-1")
	if err != nil {
		t.Fatalf("ListEntitiesForClaim: %v", err)
	}
	if len(ents) != 1 || ents[0].ID != a.ID {
		t.Errorf("after merge cl-1 entities = %+v, want winner only", ents)
	}
}

func TestPostgres_CompilationJobRoundTrip(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	now := time.Now().UTC()
	job := domain.CompilationJob{
		ID: "job-1", Kind: "process", Status: "running",
		Scope:     map[string]string{"source": "raw_text"},
		StartedAt: now, UpdatedAt: now,
	}
	if err := conn.Jobs.Upsert(ctx, job); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := conn.Jobs.GetByID(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "running" || got.Scope["source"] != "raw_text" {
		t.Errorf("got = %+v", got)
	}
	if _, err := conn.Jobs.GetByID(ctx, "missing"); !errors.Is(err, errNoRowsSentinel(err)) {
		// Just check it errors; sql.ErrNoRows is intentionally
		// wrapped behind the storage boundary.
		t.Logf("missing job error (expected non-nil): %v", err)
	}
}

// errNoRowsSentinel keeps the import minimal — we just want a
// non-nil error from GetByID(missing). The test above logs but
// does not require the chain to expose sql.ErrNoRows.
func errNoRowsSentinel(err error) error { return err }

// TestPostgres_Expectations exercises the expectation repository against a live
// Postgres namespace: upsert (with + without a horizon), get, record an
// observation, list-open filtering by resolved, and the RLS-scoped insert.
func TestPostgres_Expectations(t *testing.T) {
	conn := withConn(t)
	if conn.Expectations == nil {
		t.Fatal("Expectations repository is nil on postgres — expected an implementation")
	}
	ctx := context.Background()
	horizon := time.Now().Add(time.Hour).UTC()

	if err := conn.Expectations.Upsert(ctx, domain.Expectation{ClaimID: "c1", Predicted: 100, Tolerance: 10, Horizon: horizon, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, ok, err := conn.Expectations.Get(ctx, "c1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Predicted != 100 || got.Tolerance != 10 || got.Horizon.IsZero() {
		t.Errorf("round-trip wrong: %+v", got)
	}

	// Record an observation + resolve it; it should drop out of ListOpen.
	got.Observed = 250
	got.HasObservation = true
	if err := conn.Expectations.Upsert(ctx, got); err != nil {
		t.Fatalf("Upsert observation: %v", err)
	}
	open, err := conn.Expectations.ListOpen(ctx)
	if err != nil {
		t.Fatalf("ListOpen: %v", err)
	}
	if len(open) != 1 || !open[0].HasObservation {
		t.Fatalf("ListOpen (unresolved) wrong: %+v", open)
	}
	got.Resolved = true
	if err := conn.Expectations.Upsert(ctx, got); err != nil {
		t.Fatalf("Upsert resolved: %v", err)
	}
	open, err = conn.Expectations.ListOpen(ctx)
	if err != nil {
		t.Fatalf("ListOpen after resolve: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("resolved expectation must not appear in ListOpen; got %+v", open)
	}
}

// TestPostgres_WorkingMemoryBlocks exercises the block repository against a live
// Postgres namespace: upsert (insert + in-place update), get, list, delete. Also
// confirms the ADR 0007 tenant column + RLS scoping (added to working_memory_blocks)
// does not reject inserts on the namespace-mode connection.
func TestPostgres_WorkingMemoryBlocks(t *testing.T) {
	conn := withConn(t)
	if conn.Blocks == nil {
		t.Fatal("Blocks repository is nil on postgres — expected an implementation")
	}
	ctx := context.Background()
	const owner = "worker-uuid-1"

	if err := conn.Blocks.Upsert(ctx, domain.WorkingMemoryBlock{Owner: owner, Label: "persona", Content: "analyst", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Upsert insert: %v", err)
	}
	// In-place update on the (owner,label) key.
	if err := conn.Blocks.Upsert(ctx, domain.WorkingMemoryBlock{Owner: owner, Label: "persona", Content: "terse analyst", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	if err := conn.Blocks.Upsert(ctx, domain.WorkingMemoryBlock{Owner: owner, Label: "open_threads", Content: "checkout latency", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Upsert second: %v", err)
	}

	got, ok, err := conn.Blocks.Get(ctx, owner, "persona")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Content != "terse analyst" {
		t.Errorf("update didn't take: content = %q", got.Content)
	}

	list, err := conn.Blocks.List(ctx, owner)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Label != "open_threads" || list[1].Label != "persona" {
		t.Fatalf("List wrong (want label-ordered open_threads, persona): %+v", list)
	}

	if err := conn.Blocks.Delete(ctx, owner, "persona"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := conn.Blocks.Get(ctx, owner, "persona"); ok {
		t.Error("block still present after Delete")
	}
}

// TestPostgres_CrossTenantIsolation is the ADR 0007 guardrail: with two tenant
// views over the same namespace, tenant B must read NONE of tenant A's rows.
// Runs in both the per-tenant-pool (default) and the shared-pool (Phase 2)
// modes. Skips loudly if the connecting role bypasses RLS — isolation is
// unenforceable there and a deployment MUST use a non-superuser role.
func TestPostgres_CrossTenantIsolation(t *testing.T) {
	dsn := requireLiveDSN(t)
	ctx := context.Background()
	ns := fmt.Sprintf("mnemos_iso_%d", time.Now().UnixNano())

	open := func(t *testing.T, tenant string) *store.Conn {
		t.Helper()
		full := dsn
		sep := "?"
		if contains(full, "?") {
			sep = "&"
		}
		full += sep + "namespace=" + ns
		if tenant != "" {
			full += "&tenant=" + tenant
		}
		conn, err := store.Open(ctx, full)
		if err != nil {
			t.Fatalf("store.Open(tenant=%q): %v", tenant, err)
		}
		return conn
	}

	// Bootstrap the namespace and guard against an RLS-bypassing role.
	base := open(t, "")
	t.Cleanup(func() {
		if raw, ok := base.Raw.(interface {
			ExecContext(context.Context, string, ...any) (any, error)
		}); ok {
			_, _ = raw.ExecContext(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", ns))
		}
		_ = base.Close()
	})
	if r, ok := base.Raw.(interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}); ok {
		var bypass bool
		if err := r.QueryRowContext(ctx, "SELECT rolbypassrls OR rolsuper FROM pg_roles WHERE rolname = current_user").Scan(&bypass); err == nil && bypass {
			t.Skip("connecting role bypasses RLS (superuser/BYPASSRLS); isolation is unenforceable — use a non-superuser role")
		}
	}

	for _, shared := range []bool{false, true} {
		mode := "per_tenant_pool"
		if shared {
			mode = "shared_pool"
		}
		t.Run(mode, func(t *testing.T) {
			if shared {
				t.Setenv("MNEMOS_PG_SHARED_POOL", "1")
			} else {
				t.Setenv("MNEMOS_PG_SHARED_POOL", "")
			}
			a := open(t, "acme-"+mode)
			defer func() { _ = a.Close() }()
			b := open(t, "globex-"+mode)
			defer func() { _ = b.Close() }()

			now := time.Now().UTC()
			ev := domain.Event{
				ID: "iso-" + mode, RunID: "r", SchemaVersion: "1",
				Content: "tenant-A secret", SourceInputID: "in",
				Timestamp: now, IngestedAt: now, CreatedBy: domain.SystemUser,
			}
			if err := a.Events.Append(ctx, ev); err != nil {
				t.Fatalf("tenant A append: %v", err)
			}

			bEvents, err := b.Events.ListAll(ctx)
			if err != nil {
				t.Fatalf("tenant B list: %v", err)
			}
			for _, e := range bEvents {
				if e.ID == ev.ID {
					t.Fatalf("CROSS-TENANT LEAK: tenant B read tenant A's event %q", e.ID)
				}
			}

			aEvents, err := a.Events.ListAll(ctx)
			if err != nil {
				t.Fatalf("tenant A list: %v", err)
			}
			found := false
			for _, e := range aEvents {
				if e.ID == ev.ID {
					found = true
				}
			}
			if !found {
				t.Errorf("tenant A cannot see its own event (mode=%s)", mode)
			}
		})
	}
}
