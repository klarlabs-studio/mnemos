package memory_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"

	// Register the memory provider for the smoke test.
	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// openMemory is a tiny helper that runs every behaviour test through
// the same store.Open path the rest of the codebase uses, so the
// tests double as registry smoke coverage.
func openMemory(t *testing.T) *store.Conn {
	t.Helper()
	conn, err := store.Open(context.Background(), "memory://")
	if err != nil {
		t.Fatalf("store.Open(memory://): %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("conn.Close: %v", err)
		}
	})
	return conn
}

func TestOpen_DefaultNamespace(t *testing.T) {
	t.Parallel()
	conn, err := store.Open(context.Background(), "memory://")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = conn.Close() }()
}

func TestOpen_ExplicitNamespace(t *testing.T) {
	t.Parallel()
	conn, err := store.Open(context.Background(), "memory://?namespace=chronos")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = conn.Close() }()
}

func TestOpen_InvalidNamespaceRejected(t *testing.T) {
	t.Parallel()
	_, err := store.Open(context.Background(), "memory://?namespace=Team-X")
	if err == nil {
		t.Fatal("expected error for invalid namespace, got nil")
	}
}

func TestOpen_PopulatesAllRepositories(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)

	if conn.Events == nil {
		t.Error("Conn.Events is nil")
	}
	if conn.Claims == nil {
		t.Error("Conn.Claims is nil")
	}
	if conn.Relationships == nil {
		t.Error("Conn.Relationships is nil")
	}
	if conn.Embeddings == nil {
		t.Error("Conn.Embeddings is nil")
	}
	if conn.Users == nil {
		t.Error("Conn.Users is nil")
	}
	if conn.RevokedTokens == nil {
		t.Error("Conn.RevokedTokens is nil")
	}
	if conn.Agents == nil {
		t.Error("Conn.Agents is nil")
	}
	if conn.Entities == nil {
		t.Error("Conn.Entities is nil")
	}
	if conn.Jobs == nil {
		t.Error("Conn.Jobs is nil")
	}
}

func TestOpen_InstancesAreIsolated(t *testing.T) {
	t.Parallel()
	a := openMemory(t)
	b := openMemory(t)

	ctx := context.Background()
	ev := newEvent("ev-iso", "run-iso", "isolated content")
	if err := a.Events.Append(ctx, ev); err != nil {
		t.Fatalf("a.Events.Append: %v", err)
	}

	got, err := b.Events.ListAll(ctx)
	if err != nil {
		t.Fatalf("b.Events.ListAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected fresh memory Conn to be empty, got %d events", len(got))
	}
}

func TestEventRepository_AppendListGetByRun(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)
	ctx := context.Background()

	a := newEvent("ev-1", "run-A", "alpha")
	b := newEvent("ev-2", "run-B", "beta")
	c := newEvent("ev-3", "run-A", "gamma")
	for _, ev := range []domain.Event{a, b, c} {
		if err := conn.Events.Append(ctx, ev); err != nil {
			t.Fatalf("Append %s: %v", ev.ID, err)
		}
	}

	// Re-appending the same event id is idempotent.
	if err := conn.Events.Append(ctx, a); err != nil {
		t.Fatalf("Append idempotency: %v", err)
	}

	got, err := conn.Events.GetByID(ctx, "ev-2")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Content != "beta" {
		t.Errorf("GetByID content = %q, want %q", got.Content, "beta")
	}

	all, err := conn.Events.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ListAll len = %d, want 3", len(all))
	}

	runA, err := conn.Events.ListByRunID(ctx, "run-A")
	if err != nil {
		t.Fatalf("ListByRunID: %v", err)
	}
	if len(runA) != 2 {
		t.Errorf("ListByRunID len = %d, want 2", len(runA))
	}

	byIDs, err := conn.Events.ListByIDs(ctx, []string{"ev-3", "missing", "ev-1"})
	if err != nil {
		t.Fatalf("ListByIDs: %v", err)
	}
	if len(byIDs) != 2 || byIDs[0].ID != "ev-3" || byIDs[1].ID != "ev-1" {
		t.Errorf("ListByIDs = %+v, want [ev-3, ev-1] in order", byIDs)
	}
}

func TestEventRepository_ValidateRejectsBadRows(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)
	ctx := context.Background()
	if err := conn.Events.Append(ctx, domain.Event{}); err == nil {
		t.Error("Append accepted empty event; expected validation error")
	}
}

func TestClaimRepository_UpsertEvidenceAndStatusHistory(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)
	ctx := context.Background()

	if err := conn.Events.Append(ctx, newEvent("ev-1", "run-A", "source")); err != nil {
		t.Fatalf("Append event: %v", err)
	}

	created := time.Now().UTC().Add(-time.Hour)
	cl := domain.Claim{
		ID:         "cl-1",
		Text:       "the sky is blue",
		Type:       domain.ClaimTypeFact,
		Confidence: 0.9,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  created,
		CreatedBy:  "u-test",
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{cl}); err != nil {
		t.Fatalf("Upsert active: %v", err)
	}
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: "cl-1", EventID: "ev-1"}}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}

	// Same status -> no new transition row.
	if err := conn.Claims.Upsert(ctx, []domain.Claim{cl}); err != nil {
		t.Fatalf("Upsert same status: %v", err)
	}

	// Status change -> transition recorded.
	cl.Status = domain.ClaimStatusContested
	if err := conn.Claims.UpsertWithReasonAs(ctx, []domain.Claim{cl}, "auto: contradiction", "u-actor"); err != nil {
		t.Fatalf("Upsert contested: %v", err)
	}

	hist, err := conn.Claims.ListStatusHistoryByClaimID(ctx, "cl-1")
	if err != nil {
		t.Fatalf("ListStatusHistoryByClaimID: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("history len = %d, want 2 (initial + contested)", len(hist))
	}
	if hist[0].FromStatus != "" || hist[0].ToStatus != domain.ClaimStatusActive {
		t.Errorf("first transition = %+v, want '' -> active", hist[0])
	}
	if hist[1].FromStatus != domain.ClaimStatusActive || hist[1].ToStatus != domain.ClaimStatusContested {
		t.Errorf("second transition = %+v, want active -> contested", hist[1])
	}
	if hist[1].Reason != "auto: contradiction" {
		t.Errorf("reason = %q, want %q", hist[1].Reason, "auto: contradiction")
	}
	if hist[1].ChangedBy != "u-actor" {
		t.Errorf("changedBy = %q, want u-actor", hist[1].ChangedBy)
	}

	byEvents, err := conn.Claims.ListByEventIDs(ctx, []string{"ev-1"})
	if err != nil {
		t.Fatalf("ListByEventIDs: %v", err)
	}
	if len(byEvents) != 1 || byEvents[0].ID != "cl-1" {
		t.Errorf("ListByEventIDs = %+v, want [cl-1]", byEvents)
	}

	evLinks, err := conn.Claims.ListEvidenceByClaimIDs(ctx, []string{"cl-1"})
	if err != nil {
		t.Fatalf("ListEvidenceByClaimIDs: %v", err)
	}
	if len(evLinks) != 1 || evLinks[0].EventID != "ev-1" {
		t.Errorf("evidence = %+v, want [{cl-1, ev-1}]", evLinks)
	}
}

func TestClaimRepository_TrustScorerCapability(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)
	ctx := context.Background()

	scorer, ok := conn.Claims.(ports.TrustScorer)
	if !ok {
		t.Fatal("memory ClaimRepository does not satisfy ports.TrustScorer")
	}

	if err := conn.Events.Append(ctx, newEvent("ev-T", "run", "anything")); err != nil {
		t.Fatalf("Append event: %v", err)
	}
	cl := domain.Claim{
		ID: "cl-T", Text: "trust me", Type: domain.ClaimTypeFact,
		Confidence: 0.6, Status: domain.ClaimStatusActive,
		CreatedAt: time.Now().UTC(),
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{cl}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: "cl-T", EventID: "ev-T"}}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}

	n, err := scorer.RecomputeTrust(ctx, func(confidence float64, evidenceCount int, _ time.Time) float64 {
		return confidence * float64(evidenceCount)
	})
	if err != nil {
		t.Fatalf("RecomputeTrust: %v", err)
	}
	if n != 1 {
		t.Errorf("RecomputeTrust touched %d claims, want 1", n)
	}

	avg, err := scorer.AverageTrust(ctx)
	if err != nil {
		t.Fatalf("AverageTrust: %v", err)
	}
	if avg <= 0 {
		t.Errorf("AverageTrust = %v, want >0", avg)
	}

	low, err := scorer.CountClaimsBelowTrust(ctx, 999)
	if err != nil {
		t.Fatalf("CountClaimsBelowTrust: %v", err)
	}
	if low != 1 {
		t.Errorf("CountClaimsBelowTrust(999) = %d, want 1", low)
	}
}

// TestClaimRepository_IndependenceAwareCorroboration is the echo-chamber guard:
// three events from three DISTINCT authors corroborate more than three events from
// ONE author (whose repeats are discounted), even though the raw event count is
// identical.
func TestClaimRepository_IndependenceAwareCorroboration(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)
	ctx := context.Background()
	scorer := conn.Claims.(ports.TrustScorer)

	mkEvent := func(id, author string) domain.Event {
		e := newEvent(id, "run", "content "+id)
		e.CreatedBy = author
		return e
	}
	for i, a := range []string{"alice", "bob", "carol"} { // claim A: three voices
		if err := conn.Events.Append(ctx, mkEvent(fmt.Sprintf("evA%d", i), a)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ { // claim B: one voice, thrice
		if err := conn.Events.Append(ctx, mkEvent(fmt.Sprintf("evB%d", i), "dave")); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	if err := conn.Claims.Upsert(ctx, []domain.Claim{
		{ID: "clA", Text: "three voices", Type: domain.ClaimTypeFact, Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: now},
		{ID: "clB", Text: "one voice thrice", Type: domain.ClaimTypeFact, Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	links := []domain.ClaimEvidence{
		{ClaimID: "clA", EventID: "evA0"}, {ClaimID: "clA", EventID: "evA1"}, {ClaimID: "clA", EventID: "evA2"},
		{ClaimID: "clB", EventID: "evB0"}, {ClaimID: "clB", EventID: "evB1"}, {ClaimID: "clB", EventID: "evB2"},
	}
	if err := conn.Claims.UpsertEvidence(ctx, links); err != nil {
		t.Fatal(err)
	}

	// score = the graded evidence count, to isolate corroboration.
	if _, err := scorer.RecomputeTrust(ctx, func(_ float64, evidenceCount int, _ time.Time) float64 {
		return float64(evidenceCount)
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
	if trust["clA"] != 3 { // 3 distinct sources → full credit
		t.Errorf("clA (three independent voices) graded count = %v, want 3", trust["clA"])
	}
	if trust["clB"] != 2 { // 1 source, 3 events → 1 + floor(0.5*2) = 2
		t.Errorf("clB (one voice thrice) graded count = %v, want 2 (repeats discounted)", trust["clB"])
	}
	if trust["clA"] <= trust["clB"] {
		t.Fatal("three independent voices must corroborate more than one voice repeated")
	}
}

func TestClaimRepository_SetValidity(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)
	ctx := context.Background()

	cl := domain.Claim{
		ID: "cl-V", Text: "valid", Type: domain.ClaimTypeFact,
		Confidence: 0.5, Status: domain.ClaimStatusActive,
		CreatedAt: time.Now().UTC(),
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{cl}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	cutoff := time.Now().UTC()
	if err := conn.Claims.SetValidity(ctx, "cl-V", cutoff); err != nil {
		t.Fatalf("SetValidity: %v", err)
	}
	got, err := conn.Claims.ListByIDs(ctx, []string{"cl-V"})
	if err != nil {
		t.Fatalf("ListByIDs: %v", err)
	}
	if len(got) != 1 || got[0].ValidTo.IsZero() {
		t.Fatalf("after SetValidity, ValidTo not set: %+v", got)
	}
	if err := conn.Claims.SetValidity(ctx, "cl-V", time.Time{}); err != nil {
		t.Fatalf("SetValidity zero: %v", err)
	}
	got, _ = conn.Claims.ListByIDs(ctx, []string{"cl-V"})
	if !got[0].ValidTo.IsZero() {
		t.Errorf("after SetValidity(zero), ValidTo = %v, want zero", got[0].ValidTo)
	}

	if err := conn.Claims.SetValidity(ctx, "missing", cutoff); err == nil {
		t.Error("SetValidity on missing claim accepted; expected error")
	}
}

func TestRelationshipRepository_ListByClaim(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)
	ctx := context.Background()

	rels := []domain.Relationship{
		{ID: "r1", Type: domain.RelationshipTypeSupports, FromClaimID: "a", ToClaimID: "b", CreatedAt: time.Now().UTC()},
		{ID: "r2", Type: domain.RelationshipTypeContradicts, FromClaimID: "b", ToClaimID: "c", CreatedAt: time.Now().UTC()},
		{ID: "r3", Type: domain.RelationshipTypeSupports, FromClaimID: "d", ToClaimID: "a", CreatedAt: time.Now().UTC()},
	}
	if err := conn.Relationships.Upsert(ctx, rels); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	byA, err := conn.Relationships.ListByClaim(ctx, "a")
	if err != nil {
		t.Fatalf("ListByClaim: %v", err)
	}
	if len(byA) != 2 {
		t.Errorf("ListByClaim(a) len = %d, want 2 (r1, r3)", len(byA))
	}

	byBC, err := conn.Relationships.ListByClaimIDs(ctx, []string{"b", "c"})
	if err != nil {
		t.Fatalf("ListByClaimIDs: %v", err)
	}
	if len(byBC) != 2 {
		t.Errorf("ListByClaimIDs(b,c) len = %d, want 2 (r1, r2)", len(byBC))
	}
}

func TestEmbeddingRepository_RoundTrip(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)
	ctx := context.Background()

	vec := []float32{0.1, 0.2, 0.3}
	if err := conn.Embeddings.Upsert(ctx, "ev-E", "event", vec, "model-X", ""); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Mutating the input slice must not corrupt the stored copy.
	vec[0] = 99.0

	rows, err := conn.Embeddings.ListByEntityType(ctx, "event")
	if err != nil {
		t.Fatalf("ListByEntityType: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListByEntityType len = %d, want 1", len(rows))
	}
	if rows[0].Vector[0] != 0.1 {
		t.Errorf("stored vector mutated through caller alias: got %v", rows[0].Vector)
	}
	if rows[0].Dimensions != 3 || rows[0].Model != "model-X" {
		t.Errorf("metadata = %+v, want dims=3 model=model-X", rows[0])
	}
}

func TestUserRepository_CreateAndLookup(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)
	ctx := context.Background()

	u := domain.User{ID: "u-1", Name: "Alice", Email: "a@x", Status: domain.UserStatusActive, CreatedAt: time.Now().UTC()}
	if err := conn.Users.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := conn.Users.Create(ctx, u); err == nil {
		t.Error("duplicate Create accepted; expected error")
	}
	dup := u
	dup.ID = "u-2"
	if err := conn.Users.Create(ctx, dup); err == nil {
		t.Error("Create with duplicate email accepted; expected error")
	}

	gotByID, err := conn.Users.GetByID(ctx, "u-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if gotByID.Name != "Alice" {
		t.Errorf("GetByID Name = %q", gotByID.Name)
	}
	gotByEmail, err := conn.Users.GetByEmail(ctx, "a@x")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if gotByEmail.ID != "u-1" {
		t.Errorf("GetByEmail ID = %q", gotByEmail.ID)
	}

	if _, err := conn.Users.GetByID(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetByID(missing) error = %v, want wrapping sql.ErrNoRows", err)
	}
	if err := conn.Users.UpdateStatus(ctx, "u-1", domain.UserStatusRevoked); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if err := conn.Users.UpdateScopes(ctx, "u-1", []string{"events:write"}); err != nil {
		t.Fatalf("UpdateScopes: %v", err)
	}

	all, err := conn.Users.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("List len = %d, want 1", len(all))
	}
	if all[0].Status != domain.UserStatusRevoked || len(all[0].Scopes) != 1 {
		t.Errorf("after updates: %+v", all[0])
	}
}

func TestAgentRepository_RequiresKnownOwner(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)
	ctx := context.Background()

	if err := conn.Agents.Create(ctx, domain.Agent{
		ID: "a-1", Name: "ci-runner", OwnerID: "u-missing",
		Status: domain.AgentStatusActive, CreatedAt: time.Now().UTC(),
	}); err == nil {
		t.Error("Create with unknown owner accepted; expected error")
	}

	if err := conn.Users.Create(ctx, domain.User{
		ID: "u-1", Name: "owner", Email: "o@x", Status: domain.UserStatusActive, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := conn.Agents.Create(ctx, domain.Agent{
		ID: "a-1", Name: "ci-runner", OwnerID: "u-1",
		Scopes:    []string{"events:write"},
		Status:    domain.AgentStatusActive,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := conn.Agents.UpdateAllowedRuns(ctx, "a-1", []string{"run-prod"}); err != nil {
		t.Fatalf("UpdateAllowedRuns: %v", err)
	}
	got, err := conn.Agents.GetByID(ctx, "a-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(got.AllowedRuns) != 1 || got.AllowedRuns[0] != "run-prod" {
		t.Errorf("AllowedRuns = %v", got.AllowedRuns)
	}
}

func TestRevokedTokenRepository_AddIsRevokedPurge(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)
	ctx := context.Background()

	now := time.Now().UTC()
	tok := domain.RevokedToken{JTI: "jti-1", RevokedAt: now, ExpiresAt: now.Add(time.Hour)}
	old := domain.RevokedToken{JTI: "jti-old", RevokedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}

	if err := conn.RevokedTokens.Add(ctx, tok); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := conn.RevokedTokens.Add(ctx, tok); err != nil {
		t.Fatalf("Add idempotent: %v", err)
	}
	if err := conn.RevokedTokens.Add(ctx, old); err != nil {
		t.Fatalf("Add old: %v", err)
	}

	if ok, _ := conn.RevokedTokens.IsRevoked(ctx, "jti-1"); !ok {
		t.Error("jti-1 should be revoked")
	}
	if ok, _ := conn.RevokedTokens.IsRevoked(ctx, "jti-other"); ok {
		t.Error("jti-other should not be revoked")
	}

	n, err := conn.RevokedTokens.PurgeExpired(ctx, now)
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("PurgeExpired removed %d, want 1", n)
	}
	if ok, _ := conn.RevokedTokens.IsRevoked(ctx, "jti-old"); ok {
		t.Error("jti-old should be purged")
	}
}

func TestEntityRepository_FindOrCreateAndLink(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)
	ctx := context.Background()

	// FindOrCreate is the dedup contract: same (normalized_name, type)
	// returns the same id, regardless of casing.
	a, err := conn.Entities.FindOrCreate(ctx, "Felix Geelhaar", domain.EntityTypePerson, "u-test")
	if err != nil {
		t.Fatalf("FindOrCreate: %v", err)
	}
	b, err := conn.Entities.FindOrCreate(ctx, "  felix   geelhaar  ", domain.EntityTypePerson, "u-test")
	if err != nil {
		t.Fatalf("FindOrCreate idempotent: %v", err)
	}
	if a.ID != b.ID {
		t.Errorf("FindOrCreate id collision: a=%s b=%s (both Felix Geelhaar / person)", a.ID, b.ID)
	}

	// Different type with the same name is a different entity.
	c, err := conn.Entities.FindOrCreate(ctx, "Felix Geelhaar", domain.EntityTypeOrg, "u-test")
	if err != nil {
		t.Fatalf("FindOrCreate diff type: %v", err)
	}
	if c.ID == a.ID {
		t.Errorf("FindOrCreate collapsed across types: %+v", c)
	}

	// LinkClaim is idempotent on the (claim, entity, role) key.
	if err := conn.Entities.LinkClaim(ctx, "cl-1", a.ID, "subject"); err != nil {
		t.Fatalf("LinkClaim: %v", err)
	}
	if err := conn.Entities.LinkClaim(ctx, "cl-1", a.ID, "subject"); err != nil {
		t.Fatalf("LinkClaim idempotent: %v", err)
	}

	count, err := conn.Entities.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 2 {
		t.Errorf("Count = %d, want 2 (Felix Geelhaar / person + Felix Geelhaar / org)", count)
	}

	// FindByName returns the first hit by insertion order.
	got, ok, err := conn.Entities.FindByName(ctx, "FELIX GEELHAAR")
	if err != nil {
		t.Fatalf("FindByName: %v", err)
	}
	if !ok || got.ID != a.ID {
		t.Errorf("FindByName = %+v ok=%v, want id=%s ok=true", got, ok, a.ID)
	}

	// ListByType returns only matching types.
	persons, err := conn.Entities.ListByType(ctx, domain.EntityTypePerson)
	if err != nil {
		t.Fatalf("ListByType: %v", err)
	}
	if len(persons) != 1 || persons[0].ID != a.ID {
		t.Errorf("ListByType(person) = %+v, want [%s]", persons, a.ID)
	}
}

func TestEntityRepository_MergeRewritesLinks(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)
	ctx := context.Background()

	winner, _ := conn.Entities.FindOrCreate(ctx, "Felix Geelhaar", domain.EntityTypePerson, "")
	loser, _ := conn.Entities.FindOrCreate(ctx, "felixgeelhaar", domain.EntityTypePerson, "")

	if err := conn.Entities.LinkClaim(ctx, "cl-1", loser.ID, "subject"); err != nil {
		t.Fatalf("LinkClaim loser: %v", err)
	}
	if err := conn.Entities.LinkClaim(ctx, "cl-2", winner.ID, "subject"); err != nil {
		t.Fatalf("LinkClaim winner: %v", err)
	}

	if err := conn.Entities.Merge(ctx, winner.ID, loser.ID); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	if _, hit, _ := conn.Entities.FindByName(ctx, "felixgeelhaar"); hit {
		t.Error("loser should be deleted after merge")
	}
	count, _ := conn.Entities.Count(ctx)
	if count != 1 {
		t.Errorf("Count after merge = %d, want 1", count)
	}

	// Both claim links now point at winner.
	for _, claimID := range []string{"cl-1", "cl-2"} {
		ents, _, err := conn.Entities.ListEntitiesForClaim(ctx, claimID)
		if err != nil {
			t.Fatalf("ListEntitiesForClaim(%s): %v", claimID, err)
		}
		if len(ents) != 1 || ents[0].ID != winner.ID {
			t.Errorf("after merge, claim %s links = %+v, want [winner=%s]", claimID, ents, winner.ID)
		}
	}

	if err := conn.Entities.Merge(ctx, winner.ID, winner.ID); err == nil {
		t.Error("Merge(winner, winner) accepted; expected ErrEntityMergeSelf")
	}
}

func TestEntityRepository_ClaimIDsMissingEntityLinks(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)
	ctx := context.Background()

	// Seed two claims.
	now := time.Now().UTC()
	for _, id := range []string{"cl-A", "cl-B"} {
		if err := conn.Claims.Upsert(ctx, []domain.Claim{{
			ID: id, Text: "x", Type: domain.ClaimTypeFact,
			Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: now,
		}}); err != nil {
			t.Fatalf("seed claim %s: %v", id, err)
		}
	}

	e, _ := conn.Entities.FindOrCreate(ctx, "Mnemos", domain.EntityTypeProject, "")
	if err := conn.Entities.LinkClaim(ctx, "cl-A", e.ID, "subject"); err != nil {
		t.Fatalf("LinkClaim: %v", err)
	}

	missing, err := conn.Entities.ClaimIDsMissingEntityLinks(ctx)
	if err != nil {
		t.Fatalf("ClaimIDsMissingEntityLinks: %v", err)
	}
	if len(missing) != 1 || missing[0] != "cl-B" {
		t.Errorf("missing = %v, want [cl-B]", missing)
	}
}

func TestCompilationJobRepository_RoundTrip(t *testing.T) {
	t.Parallel()
	conn := openMemory(t)
	ctx := context.Background()

	now := time.Now().UTC()
	job := domain.CompilationJob{
		ID:        "job-1",
		Kind:      "process",
		Status:    "running",
		Scope:     map[string]string{"source": "raw_text"},
		StartedAt: now,
		UpdatedAt: now,
	}
	if err := conn.Jobs.Upsert(ctx, job); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := conn.Jobs.GetByID(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "running" || got.Scope["source"] != "raw_text" {
		t.Errorf("GetByID = %+v, want status=running scope[source]=raw_text", got)
	}

	// Mutating the input scope must not corrupt the stored copy.
	job.Scope["source"] = "tampered"
	got, _ = conn.Jobs.GetByID(ctx, "job-1")
	if got.Scope["source"] != "raw_text" {
		t.Errorf("stored scope mutated through caller alias: %v", got.Scope)
	}

	// Upsert is replace-on-id.
	job.Status = "completed"
	job.Scope["source"] = "raw_text"
	if err := conn.Jobs.Upsert(ctx, job); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	got, _ = conn.Jobs.GetByID(ctx, "job-1")
	if got.Status != "completed" {
		t.Errorf("after second Upsert, status = %q, want completed", got.Status)
	}

	if _, err := conn.Jobs.GetByID(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetByID(missing) error = %v, want wrapping sql.ErrNoRows", err)
	}
}

func newEvent(id, runID, content string) domain.Event {
	now := time.Now().UTC()
	return domain.Event{
		ID:            id,
		RunID:         runID,
		SchemaVersion: "1",
		Content:       content,
		SourceInputID: "input-" + id,
		Timestamp:     now,
		IngestedAt:    now,
		CreatedBy:     domain.SystemUser,
	}
}
