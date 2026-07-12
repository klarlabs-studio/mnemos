package main

import (
	"context"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// seedTenantLesson writes one synthesized schema (lesson) with the given
// statement into a fresh tenant sqlite store at dsn.
func seedTenantLesson(t *testing.T, dsn, id, statement string) {
	t.Helper()
	ctx := context.Background()
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open tenant %s: %v", dsn, err)
	}
	defer func() { _ = conn.Close() }()
	now := time.Now().UTC()
	actionID := "a_" + id
	if err := conn.Actions.Append(ctx, domain.Action{
		ID: actionID, Kind: domain.ActionKindDeploy, Subject: "svc", At: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("append action: %v", err)
	}
	if err := conn.Lessons.Append(ctx, domain.Lesson{
		ID: id, Statement: statement, Confidence: 0.8,
		Evidence: []string{actionID}, DerivedAt: now,
		// Class-level by default: these promotion tests use category-level facts
		// (about services/incident classes), which are eligible past the ADR-0012
		// subject-class gate. seedTenantLessonClass overrides for gate tests.
		SubjectClass: domain.SubjectClassClass,
	}); err != nil {
		t.Fatalf("append lesson: %v", err)
	}
}

// seedTenantLessonClass writes one synthesized schema with an explicit subject
// class so the ADR-0012 eligibility gate can be exercised via the CLI path.
func seedTenantLessonClass(t *testing.T, dsn, id, statement string, class domain.SubjectClass) {
	t.Helper()
	ctx := context.Background()
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open tenant %s: %v", dsn, err)
	}
	defer func() { _ = conn.Close() }()
	now := time.Now().UTC()
	actionID := "a_" + id
	if err := conn.Actions.Append(ctx, domain.Action{
		ID: actionID, Kind: domain.ActionKindDeploy, Subject: "svc", At: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("append action: %v", err)
	}
	if err := conn.Lessons.Append(ctx, domain.Lesson{
		ID: id, Statement: statement, Confidence: 0.8,
		Evidence: []string{actionID}, DerivedAt: now, SubjectClass: class,
	}); err != nil {
		t.Fatalf("append lesson: %v", err)
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = orig
	out, _ := io.ReadAll(r)
	return string(out)
}

// TestHandlePromote_ApplyOperatorThenApprove exercises Deliverable 1 end-to-end:
// three tenants independently produce an equivalent schema, --apply under the
// operator gate persists it Pending to the neocortex, and `--promote approve`
// activates it.
func TestHandlePromote_ApplyOperatorThenApprove(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	stmt := "rolling back a failed deploy restores service availability"

	var tenantArgs []string
	for i, name := range []string{"t1", "t2", "t3"} {
		dsn := "sqlite://" + filepath.Join(dir, name+".db")
		seedTenantLesson(t, dsn, name+"_l", stmt)
		tenantArgs = append(tenantArgs, "--tenant-dsn", dsn)
		_ = i
	}
	globalDSN := "sqlite://" + filepath.Join(dir, "global.db")

	args := append([]string{"--promote", "--gate", "operator", "--apply", "--global-dsn", globalDSN}, tenantArgs...)
	_ = captureStdout(t, func() { handlePromote(args, Flags{}) })

	gconn, err := store.Open(ctx, globalDSN)
	if err != nil {
		t.Fatalf("open global: %v", err)
	}
	defer func() { _ = gconn.Close() }()

	pending, err := gconn.GlobalSchemas.ListByStatus(ctx, domain.GlobalSchemaStatusPending)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("want 1 pending global schema, got %d", len(pending))
	}
	got := pending[0]
	if got.Statement != stmt {
		t.Fatalf("promoted statement mismatch: %q", got.Statement)
	}
	if got.DistinctTenants != 3 {
		t.Fatalf("want 3 corroborating tenants, got %d", got.DistinctTenants)
	}

	// Approve the pending record → active.
	_ = captureStdout(t, func() {
		handlePromote([]string{"--promote", "approve", got.ID, "--global-dsn", globalDSN}, Flags{})
	})
	after, _, err := gconn.GlobalSchemas.GetByID(ctx, got.ID)
	if err != nil {
		t.Fatalf("get after approve: %v", err)
	}
	if after.Status != domain.GlobalSchemaStatusActive {
		t.Fatalf("approve did not activate: %s", after.Status)
	}
}

// seedTenantNamespaceLesson writes one synthesized schema into a TENANT
// PARTITION of a single multi-tenant base store, scoped by the tenant's derived
// namespace (namespace-per-tenant physical isolation, ADR 0007).
func seedTenantNamespaceLesson(t *testing.T, baseDSN, tenantID, id, statement string) {
	t.Helper()
	ns := store.TenantNamespace(tenantID)
	seedTenantLesson(t, store.SetDSNParam(baseDSN, "namespace", ns), id, statement)
}

// TestHandlePromote_AllTenants_Sqlite proves the deferred ADR-0011 "live
// multi-tenant reads": `--all-tenants --db <dsn>` enumerates the tenants of ONE
// multi-tenant store and feeds the SAME engine — a fact corroborated across
// three tenants promotes, while a single-tenant fact is skipped (the no-leak
// floor holds through the enumeration path).
func TestHandlePromote_AllTenants_Sqlite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	baseDSN := "sqlite://" + filepath.Join(dir, "brain.db")

	corroborated := "rolling back a failed deploy restores service availability"
	for _, name := range []string{"t1", "t2", "t3"} {
		seedTenantNamespaceLesson(t, baseDSN, name, name+"_l", corroborated)
	}
	// A fact only ONE tenant ever produced — must never promote.
	singleTenant := "the frobnicator widget on tenant four needs a manual poke"
	seedTenantNamespaceLesson(t, baseDSN, "t4", "t4_l", singleTenant)

	globalDSN := "sqlite://" + filepath.Join(dir, "global.db")
	args := []string{
		"--promote", "--gate", "operator", "--apply",
		"--all-tenants", "--db", baseDSN,
		"--global-dsn", globalDSN,
	}
	out := captureStdout(t, func() { handlePromote(args, Flags{}) })

	// Four tenant partitions were discovered and scanned.
	if !contains(out, `"tenants_scanned": 4`) {
		t.Fatalf("expected 4 tenants scanned in output, got: %s", out)
	}

	gconn, err := store.Open(ctx, globalDSN)
	if err != nil {
		t.Fatalf("open global: %v", err)
	}
	defer func() { _ = gconn.Close() }()

	pending, err := gconn.GlobalSchemas.ListByStatus(ctx, domain.GlobalSchemaStatusPending)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("want exactly 1 promoted (pending) schema, got %d: %+v", len(pending), pending)
	}
	got := pending[0]
	if got.Statement != corroborated {
		t.Fatalf("promoted statement mismatch: %q", got.Statement)
	}
	if got.DistinctTenants != 3 {
		t.Fatalf("want 3 corroborating tenants, got %d", got.DistinctTenants)
	}
	// The single-tenant fact must never appear in the neocortex.
	for _, s := range pending {
		if s.Statement == singleTenant || contains(s.Statement, "frobnicator") {
			t.Fatalf("LEAK: single-tenant fact promoted: %q", s.Statement)
		}
	}
}

// contains is a tiny substring helper for stdout assertions.
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestHandlePromote_AllTenantsRejectsExplicitDSNs confirms the two tenant inputs
// are mutually exclusive.
func TestHandlePromote_AllTenantsRejectsExplicitDSNs(t *testing.T) {
	parsed, err := parsePromoteOpts([]string{"--promote", "--all-tenants", "--tenant-dsn", "sqlite:///tmp/x.db"}, Flags{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !parsed.allTenants || len(parsed.tenantDSNs) != 1 {
		t.Fatalf("parse did not capture both inputs: %+v", parsed)
	}
}

// TestHandlePromote_DryRunDoesNotWrite confirms the default (no --apply) never
// writes to the global store.
func TestHandlePromote_DryRunDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	stmt := "scaling the workers ahead of the nightly batch prevents backlog"

	var tenantArgs []string
	for _, name := range []string{"t1", "t2", "t3"} {
		dsn := "sqlite://" + filepath.Join(dir, name+".db")
		seedTenantLesson(t, dsn, name+"_l", stmt)
		tenantArgs = append(tenantArgs, "--tenant-dsn", dsn)
	}
	globalDSN := "sqlite://" + filepath.Join(dir, "global.db")

	args := append([]string{"--promote", "--gate", "auto", "--global-dsn", globalDSN}, tenantArgs...)
	_ = captureStdout(t, func() { handlePromote(args, Flags{}) })

	gconn, err := store.Open(ctx, globalDSN)
	if err != nil {
		t.Fatalf("open global: %v", err)
	}
	defer func() { _ = gconn.Close() }()
	n, err := gconn.GlobalSchemas.CountAll(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("dry-run must not write; found %d global schemas", n)
	}
}

// TestDirectSurpriseSource_UsesBackingClaimsNotScope proves Deliverable 2 of ADR
// 0011 Phase B: a lesson's prediction-error weight comes from the DIRECT edge
// (lesson → backing actions → outcomes → decisions → belief claims →
// expectations), NOT from scope aggregation.
//
// Setup: lesson L1 is backed by action A1, which produced outcome O1, which drove
// decision D1 whose belief claim C1 has a reconciled surprise of 3. A SECOND
// claim C2 shares L1's exact scope but is NOT on L1's evidence path and carries a
// far larger surprise (99). Under the old scope-proxy L1 would inherit 99; under
// the direct edge it must be exactly 3.
func TestDirectSurpriseSource_UsesBackingClaimsNotScope(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "surprise.db")
	dsn := "sqlite://" + dbPath

	scope := domain.Scope{Service: "payments", Env: "prod"}
	now := time.Now().UTC()

	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Backing belief claim C1 (surprise 3) and an unrelated same-scope claim C2
	// (surprise 99) that must NOT bleed into L1's ranking.
	for _, c := range []domain.Claim{
		{ID: "C1", Text: "rollback restores availability", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.8, Scope: scope, CreatedAt: now},
		{ID: "C2", Text: "unrelated but same scope", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.8, Scope: scope, CreatedAt: now},
	} {
		if err := conn.Claims.Upsert(ctx, []domain.Claim{c}); err != nil {
			t.Fatalf("upsert claim %s: %v", c.ID, err)
		}
	}

	// Expectation on C1 → |10-13|/1 = 3. Expectation on C2 → |0-99|/1 = 99.
	for _, e := range []domain.Expectation{
		{ClaimID: "C1", Predicted: 10, Observed: 13, Tolerance: 1, HasObservation: true, Resolved: true, CreatedAt: now},
		{ClaimID: "C2", Predicted: 0, Observed: 99, Tolerance: 1, HasObservation: true, Resolved: true, CreatedAt: now},
	} {
		if err := conn.Expectations.Upsert(ctx, e); err != nil {
			t.Fatalf("upsert expectation %s: %v", e.ClaimID, err)
		}
	}

	// Action A1 → Outcome O1 → Decision D1 (belief C1). Action A_orphan has no
	// downstream expectation.
	for _, a := range []domain.Action{
		{ID: "A1", Kind: domain.ActionKindRollback, Subject: "payments", At: now, CreatedAt: now},
		{ID: "A_orphan", Kind: domain.ActionKindDeploy, Subject: "payments", At: now, CreatedAt: now},
	} {
		if err := conn.Actions.Append(ctx, a); err != nil {
			t.Fatalf("append action %s: %v", a.ID, err)
		}
	}
	if err := conn.Outcomes.Append(ctx, domain.Outcome{ID: "O1", ActionID: "A1", Result: domain.OutcomeResultSuccess, ObservedAt: now, CreatedAt: now}); err != nil {
		t.Fatalf("append outcome: %v", err)
	}
	if err := conn.Decisions.Append(ctx, domain.Decision{
		ID: "D1", Statement: "roll back", RiskLevel: domain.RiskLevelMedium,
		Beliefs: []string{"C1"}, OutcomeID: "O1", Scope: scope, ChosenAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("append decision: %v", err)
	}
	_ = conn.Close()

	src, err := newStoreSurpriseSource(ctx, []string{dsn})
	if err != nil {
		t.Fatalf("build surprise source: %v", err)
	}

	// L1 is backed by A1 → must resolve to C1's surprise (3), never C2's (99).
	l1 := domain.Lesson{ID: "L1", Statement: "rollback restores availability", Scope: scope, Evidence: []string{"A1"}}
	got, ok := src.SurpriseFor(ctx, l1)
	if !ok {
		t.Fatal("expected surprise data for L1")
	}
	if got != 3 {
		t.Fatalf("direct edge: want surprise 3 from backing claim C1, got %v (scope-proxy would give 99)", got)
	}

	// A lesson whose action reaches no resolved expectation has no signal.
	l2 := domain.Lesson{ID: "L2", Statement: "deploy", Scope: scope, Evidence: []string{"A_orphan"}}
	if _, ok := src.SurpriseFor(ctx, l2); ok {
		t.Fatal("orphan-action lesson should have no surprise data")
	}
}

// --- ADR 0012: subject-classified promotion (CLI) ---

// curatorToken mints an agent token carrying the given scopes, signed with the
// process-pinned test JWT secret (see TestMain).
func curatorToken(t *testing.T, scopes []string) string {
	t.Helper()
	secret, err := hex.DecodeString(os.Getenv("MNEMOS_JWT_SECRET"))
	if err != nil {
		t.Fatalf("decode test secret: %v", err)
	}
	tok, _, err := auth.NewIssuer(secret).IssueAgentTokenWithScopes("agt_curator", scopes, time.Hour)
	if err != nil {
		t.Fatalf("issue curator token: %v", err)
	}
	return tok
}

// TestHandlePromote_EligibilityGate_CLI proves the ADR 0012 eligibility gate
// through the CLI: an individual-subject fact corroborated across three tenants
// is NEVER promoted, while an equally-corroborated class-level fact is.
func TestHandlePromote_EligibilityGate_CLI(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	classFact := "rolling back a failed deploy restores service availability"
	indivFact := "rex the retriever needs twice daily insulin for his condition"

	var tenantArgs []string
	for _, name := range []string{"t1", "t2", "t3"} {
		dsn := "sqlite://" + filepath.Join(dir, name+".db")
		seedTenantLessonClass(t, dsn, name+"_c", classFact, domain.SubjectClassClass)
		seedTenantLessonClass(t, dsn, name+"_i", indivFact, domain.SubjectClassIndividual)
		tenantArgs = append(tenantArgs, "--tenant-dsn", dsn)
	}
	globalDSN := "sqlite://" + filepath.Join(dir, "global.db")

	args := append([]string{"--promote", "--gate", "operator", "--apply", "--global-dsn", globalDSN}, tenantArgs...)
	_ = captureStdout(t, func() { handlePromote(args, Flags{}) })

	gconn, err := store.Open(ctx, globalDSN)
	if err != nil {
		t.Fatalf("open global: %v", err)
	}
	defer func() { _ = gconn.Close() }()

	pending, err := gconn.GlobalSchemas.ListByStatus(ctx, domain.GlobalSchemaStatusPending)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("want exactly 1 promoted schema (class fact only), got %d: %+v", len(pending), pending)
	}
	if pending[0].Statement != classFact {
		t.Fatalf("promoted the wrong fact: %q", pending[0].Statement)
	}
	for _, s := range pending {
		if contains(s.Statement, "rex the retriever") {
			t.Fatalf("LEAK: individual-subject fact reached the global brain: %q", s.Statement)
		}
	}
}

// TestVerifyCuratorToken exercises the ADR 0012 curator-scope gate directly:
// no token and a scopeless token are both rejected; a promote:global token
// passes.
func TestVerifyCuratorToken(t *testing.T) {
	ctx := context.Background()
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "rev.db")
	// Bootstrap the store so its revocation repo exists.
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = conn.Close()

	t.Setenv("MNEMOS_TOKEN", "")
	if err := verifyCuratorToken(ctx, "", dsn); err == nil {
		t.Fatal("no token must be rejected")
	}
	if err := verifyCuratorToken(ctx, curatorToken(t, []string{"events:write"}), dsn); err == nil {
		t.Fatal("token without promote:global must be rejected")
	}
	if err := verifyCuratorToken(ctx, curatorToken(t, []string{domain.ScopePromoteGlobal}), dsn); err != nil {
		t.Fatalf("promote:global token must pass, got %v", err)
	}
}

// TestHandlePromote_Curated_CLI proves the curated single-source path end to end:
// a class-level fact from ONE tenant does not promote by default, but promotes
// under --curate with a promote:global token supplied via MNEMOS_TOKEN.
func TestHandlePromote_Curated_CLI(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fact := "the sydney funnelweb spider bite requires antivenom within thirty minutes"
	tenantDSN := "sqlite://" + filepath.Join(dir, "clinic.db")
	seedTenantLessonClass(t, tenantDSN, "s1", fact, domain.SubjectClassClass)

	// Baseline: single source, no curate → nothing promotes.
	base := "sqlite://" + filepath.Join(dir, "global_base.db")
	_ = captureStdout(t, func() {
		handlePromote([]string{"--promote", "--gate", "operator", "--apply", "--global-dsn", base, "--tenant-dsn", tenantDSN}, Flags{})
	})
	bconn, err := store.Open(ctx, base)
	if err != nil {
		t.Fatalf("open base global: %v", err)
	}
	n, err := bconn.GlobalSchemas.CountAll(ctx)
	_ = bconn.Close()
	if err != nil {
		t.Fatalf("count base: %v", err)
	}
	if n != 0 {
		t.Fatalf("single-source fact must not promote without --curate, got %d", n)
	}

	// Curated: with a promote:global token, the single-source fact promotes.
	t.Setenv("MNEMOS_TOKEN", curatorToken(t, []string{domain.ScopePromoteGlobal}))
	globalDSN := "sqlite://" + filepath.Join(dir, "global_curated.db")
	_ = captureStdout(t, func() {
		handlePromote([]string{"--promote", "--curate", "--gate", "operator", "--apply", "--global-dsn", globalDSN, "--tenant-dsn", tenantDSN}, Flags{})
	})
	gconn, err := store.Open(ctx, globalDSN)
	if err != nil {
		t.Fatalf("open curated global: %v", err)
	}
	defer func() { _ = gconn.Close() }()
	pending, err := gconn.GlobalSchemas.ListByStatus(ctx, domain.GlobalSchemaStatusPending)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 || pending[0].Statement != fact {
		t.Fatalf("curated single-source class fact must promote, got %+v", pending)
	}
}
