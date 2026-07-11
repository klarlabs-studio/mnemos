package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"go.klarlabs.de/mnemos/internal/domain"
)

// fakeRevokedRepo is a minimal in-memory implementation for tests.
type fakeRevokedRepo struct {
	revoked map[string]bool
}

func newFakeRevoked() *fakeRevokedRepo {
	return &fakeRevokedRepo{revoked: map[string]bool{}}
}

func (f *fakeRevokedRepo) Add(_ context.Context, t domain.RevokedToken) error {
	f.revoked[t.JTI] = true
	return nil
}
func (f *fakeRevokedRepo) IsRevoked(_ context.Context, jti string) (bool, error) {
	return f.revoked[jti], nil
}
func (f *fakeRevokedRepo) PurgeExpired(_ context.Context, _ time.Time) (int, error) {
	return 0, nil
}

func TestIssueWithTenant_RoundTrip(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	revoked := newFakeRevoked()
	user := domain.User{ID: "usr_t", Status: domain.UserStatusActive, CreatedAt: time.Now()}

	// Tenant-scoped user token carries the tnt claim.
	tok, _, err := NewIssuer(secret).IssueUserTokenWithTenant(user, "acme", time.Hour)
	if err != nil {
		t.Fatalf("IssueUserTokenWithTenant: %v", err)
	}
	claims, err := NewVerifier(secret, revoked).ParseAndValidate(context.Background(), tok)
	if err != nil {
		t.Fatalf("ParseAndValidate: %v", err)
	}
	if claims.Tenant != "acme" {
		t.Errorf("Tenant = %q, want acme", claims.Tenant)
	}

	// A plain token has an empty tenant.
	tok2, _, _ := NewIssuer(secret).IssueUserToken(user, time.Hour)
	claims2, _ := NewVerifier(secret, revoked).ParseAndValidate(context.Background(), tok2)
	if claims2.Tenant != "" {
		t.Errorf("plain token tenant = %q, want empty", claims2.Tenant)
	}
}

func TestIssueAndValidate_RoundTrip(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	revoked := newFakeRevoked()

	user := domain.User{ID: "usr_abc", Name: "Alice", Email: "a@example.com", Status: domain.UserStatusActive, CreatedAt: time.Now()}
	tok, jti, err := NewIssuer(secret).IssueUserToken(user, time.Hour)
	if err != nil {
		t.Fatalf("IssueUserToken: %v", err)
	}
	if tok == "" || jti == "" {
		t.Fatal("empty token or jti")
	}

	claims, err := NewVerifier(secret, revoked).ParseAndValidate(context.Background(), tok)
	if err != nil {
		t.Fatalf("ParseAndValidate: %v", err)
	}
	if claims.UserID != "usr_abc" {
		t.Errorf("UserID = %q, want usr_abc", claims.UserID)
	}
	if claims.Kind != TokenKindUser {
		t.Errorf("Kind = %q, want user", claims.Kind)
	}
	if claims.ID != jti {
		t.Errorf("jti mismatch: claim=%s issued=%s", claims.ID, jti)
	}
}

func TestValidate_RejectsExpiredToken(t *testing.T) {
	secret, _ := GenerateSecret()
	revoked := newFakeRevoked()

	// Hand-construct an already-expired token.
	now := time.Now()
	claims := Claims{
		UserID: "usr_x",
		Kind:   TokenKindUser,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        "expired-jti",
			IssuedAt:  jwt.NewNumericDate(now.Add(-2 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(now.Add(-1 * time.Hour)),
		},
	}
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)

	_, err := NewVerifier(secret, revoked).ParseAndValidate(context.Background(), tok)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("got %v, want expiry error", err)
	}
}

func TestValidate_RejectsRevokedToken(t *testing.T) {
	secret, _ := GenerateSecret()
	revoked := newFakeRevoked()

	user := domain.User{ID: "usr_revoke", Name: "X", Email: "x@x.com", Status: domain.UserStatusActive}
	tok, jti, _ := NewIssuer(secret).IssueUserToken(user, time.Hour)
	revoked.revoked[jti] = true

	_, err := NewVerifier(secret, revoked).ParseAndValidate(context.Background(), tok)
	if err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("got %v, want revoked error", err)
	}
}

func TestValidate_RejectsWrongSecret(t *testing.T) {
	secret1, _ := GenerateSecret()
	secret2, _ := GenerateSecret()
	revoked := newFakeRevoked()

	user := domain.User{ID: "usr_y", Name: "Y", Email: "y@y.com", Status: domain.UserStatusActive}
	tok, _, _ := NewIssuer(secret1).IssueUserToken(user, time.Hour)

	_, err := NewVerifier(secret2, revoked).ParseAndValidate(context.Background(), tok)
	if err == nil {
		t.Fatal("expected signature mismatch error")
	}
}

func TestValidate_RejectsAlgConfusion(t *testing.T) {
	secret, _ := GenerateSecret()
	revoked := newFakeRevoked()

	// Construct an alg=none token (no signing). HS256 verifier must
	// reject this even though it's structurally valid JWT.
	claims := Claims{UserID: "attacker", RegisteredClaims: jwt.RegisteredClaims{
		ID:        "evil",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}}
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)

	_, err := NewVerifier(secret, revoked).ParseAndValidate(context.Background(), tok)
	if err == nil {
		t.Fatal("expected alg=none rejection")
	}
}

func TestIssueAgentToken_KindIsAgent(t *testing.T) {
	secret, _ := GenerateSecret()
	revoked := newFakeRevoked()

	tok, _, err := NewIssuer(secret).IssueAgentToken("agt_1", time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	claims, err := NewVerifier(secret, revoked).ParseAndValidate(context.Background(), tok)
	if err != nil {
		t.Fatalf("ParseAndValidate: %v", err)
	}
	if claims.Kind != TokenKindAgent {
		t.Errorf("Kind = %q, want agent", claims.Kind)
	}
}

func TestIssue_RejectsZeroTTL(t *testing.T) {
	secret, _ := GenerateSecret()
	user := domain.User{ID: "usr_z", Name: "Z", Email: "z@z.com", Status: domain.UserStatusActive}
	if _, _, err := NewIssuer(secret).IssueUserToken(user, 0); err == nil {
		t.Fatal("expected error on ttl=0")
	}
}

func TestUserToken_CarriesWildcardScope(t *testing.T) {
	secret, _ := GenerateSecret()
	revoked := newFakeRevoked()
	user := domain.User{ID: "usr_w", Name: "W", Email: "w@w.com", Status: domain.UserStatusActive}

	tok, _, err := NewIssuer(secret).IssueUserToken(user, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := NewVerifier(secret, revoked).ParseAndValidate(context.Background(), tok)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !claims.HasScope("events:write") || !claims.HasScope("anything:at:all") {
		t.Errorf("user token should grant every scope via *, got %v", claims.Scopes)
	}
}

// TestUserToken_HonoursRecordedScopes is the F.3 contract: a user
// with a concrete scope list gets a token that grants exactly those
// scopes — no implicit wildcard.
func TestUserToken_HonoursRecordedScopes(t *testing.T) {
	secret, _ := GenerateSecret()
	revoked := newFakeRevoked()
	user := domain.User{
		ID: "usr_narrow", Name: "N", Email: "n@n.com",
		Status: domain.UserStatusActive,
		Scopes: []string{"events:write", "claims:write"},
	}

	tok, _, err := NewIssuer(secret).IssueUserToken(user, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := NewVerifier(secret, revoked).ParseAndValidate(context.Background(), tok)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !claims.HasScope("events:write") || !claims.HasScope("claims:write") {
		t.Errorf("granted scopes missing: %v", claims.Scopes)
	}
	if claims.HasScope("relationships:write") {
		t.Errorf("user token unexpectedly includes wildcard or extra scope: %v", claims.Scopes)
	}
}

func TestAgentTokenWithScopes_RoundTripsExactList(t *testing.T) {
	secret, _ := GenerateSecret()
	revoked := newFakeRevoked()

	tok, _, err := NewIssuer(secret).IssueAgentTokenWithScopes("agt_x", []string{"events:write", "claims:write"}, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := NewVerifier(secret, revoked).ParseAndValidate(context.Background(), tok)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !claims.HasScope("events:write") || !claims.HasScope("claims:write") {
		t.Errorf("granted scopes missing: %v", claims.Scopes)
	}
	if claims.HasScope("relationships:write") {
		t.Errorf("agent unexpectedly granted relationships:write: %v", claims.Scopes)
	}
}

func TestAgentTokenWithRuns_RoundTripsAndAllowsRun(t *testing.T) {
	secret, _ := GenerateSecret()
	revoked := newFakeRevoked()

	tok, _, err := NewIssuer(secret).IssueAgentTokenWithScopesAndRuns(
		"agt_runscope",
		[]string{"events:write"},
		[]string{"run-a", "run-b"},
		time.Hour,
	)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := NewVerifier(secret, revoked).ParseAndValidate(context.Background(), tok)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !claims.AllowsRun("run-a") || !claims.AllowsRun("run-b") {
		t.Errorf("expected run-a and run-b allowed: %v", claims.Runs)
	}
	if claims.AllowsRun("run-c") {
		t.Errorf("run-c should be rejected by whitelist: %v", claims.Runs)
	}
}

func TestAllowsTenant(t *testing.T) {
	c := Claims{Tenant: "personal", Tenants: []string{"personal", "repo_x"}}
	for _, tt := range []struct {
		tenant string
		want   bool
	}{
		{"personal", true},   // == Tenant
		{"repo_x", true},     // in allowlist
		{"repo_y", false},    // not granted
		{"", false},          // empty never allowed
		{" personal ", true}, // trimmed
	} {
		if got := c.AllowsTenant(tt.tenant); got != tt.want {
			t.Errorf("AllowsTenant(%q) = %v, want %v", tt.tenant, got, tt.want)
		}
	}
	// A token with no tenant at all allows nothing.
	if (Claims{}).AllowsTenant("anything") {
		t.Error("empty token must not allow any tenant")
	}
}

func TestEffectiveTenant_FailClosed(t *testing.T) {
	multi := Claims{Tenant: "personal", Tenants: []string{"personal", "repo_x"}}
	single := Claims{Tenant: "solo"}
	none := Claims{}

	cases := []struct {
		name      string
		c         Claims
		requested string
		want      string
		ok        bool
	}{
		{"no selection → single tenant", single, "", "solo", true},
		{"no selection, no tenant → deny", none, "", "", false},
		{"select allowed member", multi, "repo_x", "repo_x", true},
		{"select the default", multi, "personal", "personal", true},
		{"select disallowed → deny", multi, "repo_y", "", false},
		{"single token, select other → deny", single, "repo_x", "", false},
		{"multi, no selection → default tenant", multi, "", "personal", true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.c.EffectiveTenant(tt.requested)
			if got != tt.want || ok != tt.ok {
				t.Errorf("EffectiveTenant(%q) = (%q,%v), want (%q,%v)", tt.requested, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestAllowsRun_EmptyWhitelistMeansUnrestricted(t *testing.T) {
	c := Claims{} // no Runs at all
	if !c.AllowsRun("any-run-id") {
		t.Errorf("empty Runs should mean every run allowed")
	}
}

func TestAllowsRun_WildcardStarMatchesEverything(t *testing.T) {
	t.Parallel()
	c := Claims{Runs: []string{"*"}}
	if !c.AllowsRun("anything-here") {
		t.Errorf("'*' should match every run")
	}
}

func TestAllowsRun_GlobPatternsMatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern string
		runID   string
		want    bool
	}{
		{"prod-*", "prod-2026-04-30", true},
		{"prod-*", "staging-2026-04-30", false},
		{"nightly-?-2026", "nightly-a-2026", true},
		{"nightly-?-2026", "nightly-ab-2026", false},
		{"release/[0-9]*", "release/42", true},
		{"release/[0-9]*", "release/abc", false},
		{"exact", "exact", true},
		{"exact", "different", false},
	}
	for _, c := range cases {
		got := (Claims{Runs: []string{c.pattern}}).AllowsRun(c.runID)
		if got != c.want {
			t.Errorf("AllowsRun(pattern=%q, run=%q) = %v, want %v", c.pattern, c.runID, got, c.want)
		}
	}
}

func TestVerifier_DualKeyRotation(t *testing.T) {
	user := domain.User{ID: "u1", Scopes: []string{"*"}}
	revoked := newFakeRevoked()

	oldSecret, _ := GenerateSecret()
	tokOld, _, err := NewIssuer(oldSecret).IssueUserToken(user, time.Hour)
	if err != nil {
		t.Fatalf("issue old: %v", err)
	}

	// Rotate: new active, old kept as previous.
	newSecret, _ := GenerateSecret()
	v := NewVerifierWithPrevious(newSecret, oldSecret, revoked)

	if _, err := v.ParseAndValidate(context.Background(), tokOld); err != nil {
		t.Errorf("token issued under previous secret should still validate: %v", err)
	}

	tokNew, _, err := NewIssuer(newSecret).IssueUserToken(user, time.Hour)
	if err != nil {
		t.Fatalf("issue new: %v", err)
	}
	if _, err := v.ParseAndValidate(context.Background(), tokNew); err != nil {
		t.Errorf("token under active secret failed: %v", err)
	}
}

func TestVerifier_RotationDoesNotMaskUnknownSecret(t *testing.T) {
	user := domain.User{ID: "u1", Scopes: []string{"*"}}
	revoked := newFakeRevoked()

	other, _ := GenerateSecret()
	tokForeign, _, _ := NewIssuer(other).IssueUserToken(user, time.Hour)

	active, _ := GenerateSecret()
	previous, _ := GenerateSecret()
	v := NewVerifierWithPrevious(active, previous, revoked)

	if _, err := v.ParseAndValidate(context.Background(), tokForeign); err == nil {
		t.Errorf("foreign-secret token must be rejected")
	}
}

func TestVerifier_RotationDoesNotBypassExpiry(t *testing.T) {
	user := domain.User{ID: "u1", Scopes: []string{"*"}}
	revoked := newFakeRevoked()

	old, _ := GenerateSecret()
	tokOld, _, err := NewIssuer(old).IssueUserToken(user, 1*time.Millisecond)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	active, _ := GenerateSecret()
	v := NewVerifierWithPrevious(active, old, revoked)

	if _, err := v.ParseAndValidate(context.Background(), tokOld); err == nil {
		t.Errorf("expired token should be rejected even when previous secret matches")
	}
}
