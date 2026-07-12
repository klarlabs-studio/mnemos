package mysql_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/mysql"
)

// requireLiveDSN gates every test in this file on TEST_MYSQL_DSN.
// CI configures it via docker-compose; developer machines without
// MySQL just see SKIP.
//
// Each test gets a unique namespace (database) so parallel runs
// don't collide and a leftover database from a previous failure
// doesn't poison the next run. Cleanup drops the database.
func requireLiveDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN not set; skipping mysql integration test")
	}
	return dsn
}

func withConn(t *testing.T) *store.Conn {
	t.Helper()
	dsn := requireLiveDSN(t)
	ns := fmt.Sprintf("mnemos_test_%d", time.Now().UnixNano())
	full := dsn
	if strings.Contains(full, "?") {
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
		// Drop the per-test database.
		if raw, ok := conn.Raw.(interface {
			ExecContext(context.Context, string, ...any) (any, error)
		}); ok {
			_, _ = raw.ExecContext(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", ns))
		}
		_ = conn.Close()
	})
	return conn
}

func TestMySQL_OpenBootstrapsSchema(t *testing.T) {
	conn := withConn(t)
	if conn.Events == nil || conn.Claims == nil || conn.Relationships == nil ||
		conn.Embeddings == nil || conn.Users == nil || conn.RevokedTokens == nil ||
		conn.Agents == nil || conn.Entities == nil || conn.Jobs == nil {
		t.Errorf("mysql Conn has nil port: %+v", conn)
	}
}

func TestMySQL_EventRoundTrip(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	ev := domain.Event{
		ID: "ev-1", RunID: "run-A", SchemaVersion: "1",
		Content: "alpha", SourceInputID: "in-1",
		Timestamp: now, IngestedAt: now,
		Metadata: map[string]string{"k": "v"}, CreatedBy: domain.SystemUser,
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
	if err != nil || len(all) != 1 {
		t.Errorf("ListAll = %v err=%v", all, err)
	}
	byRun, err := conn.Events.ListByRunID(ctx, "run-A")
	if err != nil || len(byRun) != 1 {
		t.Errorf("ListByRunID = %v err=%v", byRun, err)
	}
}

// TestMySQL_LessonSubjectClassRoundTrip verifies the ADR 0012
// subject_class column persists on the hand-written MySQL lessons repo —
// written on insert, refreshed on upsert, and read back into
// domain.Schema.SubjectClass.
func TestMySQL_LessonSubjectClassRoundTrip(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

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

	// Upsert with a different class must overwrite (VALUES(subject_class)).
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

// TestMySQL_ClaimSubjectClassRoundTrip verifies the ADR 0012 subject_class
// column persists on the hand-written MySQL claims repo — written on insert,
// refreshed on upsert (VALUES(subject_class)), and read back into
// domain.Claim.SubjectClass through the shared collectClaimRows scanner.
func TestMySQL_ClaimSubjectClassRoundTrip(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

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

	// Upsert with a different class must overwrite (VALUES(subject_class)).
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

func TestMySQL_ClaimUpsertWithStatusHistory(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	if err := conn.Events.Append(ctx, domain.Event{
		ID: "ev-1", RunID: "r", SchemaVersion: "1", Content: "x",
		SourceInputID: "in", Timestamp: now, IngestedAt: now, CreatedBy: domain.SystemUser,
	}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	claim := domain.Claim{
		ID: "cl-1", Text: "first", Type: domain.ClaimTypeFact,
		Confidence: 0.8, Status: domain.ClaimStatusActive,
		CreatedAt: now, CreatedBy: "u-test",
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: "cl-1", EventID: "ev-1"}}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}
	// Same status: no transition.
	if err := conn.Claims.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("Upsert same: %v", err)
	}
	claim.Status = domain.ClaimStatusContested
	if err := conn.Claims.UpsertWithReasonAs(ctx, []domain.Claim{claim}, "auto", "u-actor"); err != nil {
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

func TestMySQL_TrustScorerCapability(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	scorer, ok := conn.Claims.(ports.TrustScorer)
	if !ok {
		t.Fatal("mysql ClaimRepository does not satisfy ports.TrustScorer")
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := conn.Events.Append(ctx, domain.Event{
		ID: "ev-T", RunID: "r", SchemaVersion: "1", Content: "x",
		SourceInputID: "in", Timestamp: now, IngestedAt: now, CreatedBy: domain.SystemUser,
	}); err != nil {
		t.Fatalf("seed: %v", err)
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
	if err != nil || n != 1 {
		t.Fatalf("RecomputeTrust = %d err=%v, want 1 nil", n, err)
	}
	avg, err := scorer.AverageTrust(ctx)
	if err != nil || avg <= 0 {
		t.Errorf("AverageTrust = %v err=%v", avg, err)
	}
	low, err := scorer.CountClaimsBelowTrust(ctx, 999)
	if err != nil || low != 1 {
		t.Errorf("CountClaimsBelowTrust = %d err=%v", low, err)
	}
}

func TestMySQL_UserAgentRepositories(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
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

func TestMySQL_RevokedTokenPurge(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
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
	if err != nil || n != 1 {
		t.Errorf("PurgeExpired = %d err=%v", n, err)
	}
}

func TestMySQL_EntityFindOrCreateMerge(t *testing.T) {
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
		t.Errorf("dedup failed: a=%s b=%s", a.ID, b.ID)
	}
	loser, err := conn.Entities.FindOrCreate(ctx, "felixgeelhaar", domain.EntityTypePerson, "")
	if err != nil {
		t.Fatalf("FindOrCreate loser: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
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
		t.Errorf("after merge cl-1 entities = %+v", ents)
	}
}

func TestMySQL_CompilationJobRoundTrip(t *testing.T) {
	conn := withConn(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
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
}
