// Package auth implements the Mnemos identity primitives: JWT issuance,
// validation, and revocation. Tokens are HS256 with a server-side secret;
// claims carry the user id, optional kind (user|agent), and the standard
// jti/iat/exp triplet.
//
// Revocation is opt-in via a denylist (see ports.RevokedTokenRepository)
// — auth middleware looks up the jti on every request. The cost is one
// indexed SQLite read per call; in exchange we get instant revocation
// without rotating the signing secret.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// TokenKind distinguishes a human-issued token from an agent-issued one.
// The middleware path is the same; the kind is recorded for audit and
// future per-kind policy (rate limits, scope restrictions).
type TokenKind string

// Supported TokenKind values.
const (
	TokenKindUser  TokenKind = "user"
	TokenKindAgent TokenKind = "agent"
)

// Claims is the parsed shape of a Mnemos JWT.
//
// Scopes carries the authorisations granted to the bearer. User
// tokens always carry a single "*" scope (full access — there is no
// per-user scope policy yet). Agent tokens carry exactly the scope
// list recorded on the agent at issuance time.
//
// Runs is an optional whitelist that further restricts which run_ids
// the bearer may write. Empty list means "every run is allowed". A
// non-empty list gates write paths that carry a run_id (today:
// events) — anything outside the list is forbidden. The whitelist
// only narrows authority; it never grants new scopes.
type Claims struct {
	UserID string    `json:"sub"`
	Kind   TokenKind `json:"knd"`
	Scopes []string  `json:"scp,omitempty"`
	Runs   []string  `json:"run,omitempty"`
	// Tenant is the fine-grained isolation boundary (ADR 0007). When set, a
	// multi-tenant server scopes the bearer's reads/writes to this tenant via
	// the Postgres `mnemos.tenant` GUC + row-level security. Empty means the
	// token carries no tenant (a multi-tenant server denies it; a single-tenant
	// server ignores it). With a Tenants allowlist, Tenant is the default tenant
	// used when a request selects none.
	Tenant string `json:"tnt,omitempty"`
	// Tenants is an optional allowlist (ADR 0009): the set of tenants a request
	// may select via an X-Mnemos-Tenant header / gRPC metadata. It lets one
	// token federate its personal tenant with a repo tenant without weakening
	// per-request RLS isolation. Empty means "only Tenant is allowed".
	Tenants []string `json:"tnts,omitempty"`
	jwt.RegisteredClaims
}

// HasScope reports whether the claims grant the requested scope. The
// wildcard scope "*" matches everything; otherwise an exact string
// match is required.
func (c Claims) HasScope(want string) bool {
	for _, s := range c.Scopes {
		if s == "*" || s == want {
			return true
		}
	}
	return false
}

// AllowsRun reports whether the bearer may write to the given
// run_id. Empty Runs list means "no restriction" (allows every run).
// Non-empty list accepts:
//   - "*" wildcard (every run)
//   - exact match
//   - shell-glob match via [path.Match] (`prod-*`, `nightly-?-2026`,
//     `release/[0-9]*`). Patterns that fail to compile fall back to
//     exact-string compare so a malformed pattern can't grant
//     unintended access.
func (c Claims) AllowsRun(runID string) bool {
	if len(c.Runs) == 0 {
		return true
	}
	for _, pat := range c.Runs {
		if pat == "*" || pat == runID {
			return true
		}
		if !strings.ContainsAny(pat, "*?[") {
			continue // not a glob; exact match already failed above
		}
		matched, err := path.Match(pat, runID)
		if err == nil && matched {
			return true
		}
	}
	return false
}

// AllowsTenant reports whether the bearer may act as the given tenant: it is the
// token's Tenant, or a member of its Tenants allowlist (ADR 0009). Exact match
// only — tenants are opaque isolation boundaries, never globbed.
func (c Claims) AllowsTenant(tenant string) bool {
	tenant = strings.TrimSpace(tenant)
	if tenant == "" {
		return false
	}
	if tenant == strings.TrimSpace(c.Tenant) {
		return true
	}
	for _, t := range c.Tenants {
		if strings.TrimSpace(t) == tenant {
			return true
		}
	}
	return false
}

// EffectiveTenant resolves the tenant a request should be scoped to, given an
// optional per-request selection (X-Mnemos-Tenant header / gRPC metadata) and
// the token's grants (ADR 0009). Returns (tenant, true) when authorized,
// ("", false) when denied — fail closed:
//   - requested == "": fall back to the token's single Tenant (backward compat);
//     denied when the token carries no Tenant.
//   - requested set: authorized iff AllowsTenant(requested).
func (c Claims) EffectiveTenant(requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		t := strings.TrimSpace(c.Tenant)
		return t, t != ""
	}
	if c.AllowsTenant(requested) {
		return requested, true
	}
	return "", false
}

// tenantIDRE is the charset a tenant id must match to be safe to interpolate
// into `SET mnemos.tenant` / a derived namespace (ADR 0007): quote- and
// backslash-free. This is the single source of truth — REST, gRPC, and MCP all
// validate through ValidTenantID so the cross-tenant boundary can't drift.
var tenantIDRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// ReservedDefaultTenant is the unscoped partition. A token must never be scoped
// to it (that would grant the global/default data), so it is rejected as an
// explicit tenant even though it matches the charset.
const ReservedDefaultTenant = "__default__"

// ValidTenantID reports whether id is a well-formed, non-reserved tenant id.
func ValidTenantID(id string) bool {
	return id != ReservedDefaultTenant && tenantIDRE.MatchString(id)
}

// ResolveTenant is the one place every request-scoping surface (REST/gRPC/MCP)
// authorizes a per-request tenant selection: it applies EffectiveTenant AND the
// charset validation, returning (tenant, true) only when both pass — fail
// closed. Keeping this in one function means a future isolation hardening can't
// be applied to one surface and missed on another.
func (c Claims) ResolveTenant(requested string) (string, bool) {
	eff, ok := c.EffectiveTenant(requested)
	if !ok || !ValidTenantID(eff) {
		return "", false
	}
	return eff, true
}

// Issuer mints new JWTs. It does not store anything — the resulting
// token string is the only place the plaintext exists. Callers should
// hand it back to the user once and never persist it server-side.
//
// Issuer signs tokens with the [Keyring]'s primary kid and writes
// that kid into the JWT header so verifiers can look up the right
// secret during rotation.
type Issuer struct {
	keyring *Keyring
}

// Verifier parses + validates JWTs and consults the revocation
// denylist. The signing kid is read from the JWT header and looked up
// in the [Keyring]; empty kid (tokens minted before this surface
// existed) falls back to the primary secret for back-compat.
type Verifier struct {
	keyring *Keyring
	revoked ports.RevokedTokenRepository
}

// NewIssuer returns an Issuer signing tokens with the given secret.
// The secret becomes the primary entry of a one-key Keyring with kid
// "primary". For multi-key rotation use [NewIssuerWithKeyring].
//
// Secret should be at least 32 bytes of random data; see GenerateSecret.
func NewIssuer(secret []byte) *Issuer {
	return &Issuer{keyring: newSingleKeyKeyring(secret)}
}

// NewIssuerWithKeyring returns an Issuer that signs tokens with the
// Keyring's primary kid. Use this when the deployment is mid-rotation
// and multiple kids are valid concurrently.
func NewIssuerWithKeyring(keyring *Keyring) *Issuer {
	return &Issuer{keyring: keyring}
}

// NewVerifier returns a Verifier that validates tokens against the
// given secret and consults revoked for instant-revocation lookups.
// Wraps the secret in a one-key Keyring; for multi-key rotation use
// [NewVerifierWithKeyring].
func NewVerifier(secret []byte, revoked ports.RevokedTokenRepository) *Verifier {
	return &Verifier{keyring: newSingleKeyKeyring(secret), revoked: revoked}
}

// NewVerifierWithPrevious returns a dual-key Verifier. Tokens signed
// under either active or previous validate; new tokens (issued via
// [Issuer]) always sign under active. Pass nil/empty previous to
// disable the rotation path — callers without a rotation in flight
// should use [NewVerifier] instead.
//
// Implemented in terms of a 2-entry Keyring (kids "primary" +
// "previous"). For N-way rotation prefer [NewVerifierWithKeyring].
func NewVerifierWithPrevious(active, previous []byte, revoked ports.RevokedTokenRepository) *Verifier {
	return &Verifier{keyring: newDualKeyKeyring(active, previous), revoked: revoked}
}

// NewVerifierWithKeyring returns a Verifier that selects the signing
// secret per-request based on the JWT header's kid claim. Tokens with
// no kid header fall back to the Keyring's primary secret to remain
// compatible with the legacy single-secret + dual-key flows.
func NewVerifierWithKeyring(keyring *Keyring, revoked ports.RevokedTokenRepository) *Verifier {
	return &Verifier{keyring: keyring, revoked: revoked}
}

// IssueUserToken mints a JWT for a human user, valid for ttl.
// Returns the signed token string and the JTI (so callers that want
// to record the issuance can do so). The token's scopes mirror the
// user's recorded Scopes; an empty list is treated as the legacy
// wildcard so users created before F.3 keep working.
func (i *Issuer) IssueUserToken(user domain.User, ttl time.Duration) (token, jti string, err error) {
	return i.IssueUserTokenWithTenant(user, "", ttl)
}

// IssueUserTokenWithTenant mints a user JWT scoped to a tenant (ADR 0007). An
// empty tenant behaves exactly like IssueUserToken.
func (i *Issuer) IssueUserTokenWithTenant(user domain.User, tenant string, ttl time.Duration) (token, jti string, err error) {
	return i.IssueUserTokenWithTenants(user, tenant, nil, ttl)
}

// IssueUserTokenWithTenants mints a user JWT with a tenant + optional tenant
// allowlist (ADR 0009). tenant is the default (used when a request selects
// none); tenants is the set a request may select via X-Mnemos-Tenant. An empty
// tenant and empty tenants behave like IssueUserToken.
func (i *Issuer) IssueUserTokenWithTenants(user domain.User, tenant string, tenants []string, ttl time.Duration) (token, jti string, err error) {
	scopes := user.Scopes
	if len(scopes) == 0 {
		scopes = []string{"*"}
	}
	return i.issue(user.ID, TokenKindUser, append([]string(nil), scopes...), nil, tenant, append([]string(nil), tenants...), ttl)
}

// IssueAgentToken mints a JWT for an automated agent, valid for ttl.
// Equivalent to IssueAgentTokenWithScopes with the wildcard scope —
// useful for tests and trusted-internal use cases. New code should
// prefer IssueAgentTokenWithScopes so each agent's authority is
// explicit.
func (i *Issuer) IssueAgentToken(agentID string, ttl time.Duration) (token, jti string, err error) {
	return i.issue(agentID, TokenKindAgent, []string{"*"}, nil, "", nil, ttl)
}

// IssueAgentTokenWithScopes mints an agent JWT carrying the supplied
// scope list. Empty scopes is allowed but the resulting token can
// authorise nothing — callers usually want at least one resource
// scope (e.g., "events:write"). The run whitelist is empty (every
// run allowed) — use IssueAgentTokenWithScopesAndRuns to restrict.
func (i *Issuer) IssueAgentTokenWithScopes(agentID string, scopes []string, ttl time.Duration) (token, jti string, err error) {
	return i.issueAgent(agentID, scopes, nil, "", ttl)
}

// IssueAgentTokenWithScopesAndRuns mints an agent JWT with an
// explicit run whitelist. An empty/nil runs slice keeps the legacy
// "every run allowed" posture; a non-empty slice restricts writes
// to those run_ids.
func (i *Issuer) IssueAgentTokenWithScopesAndRuns(agentID string, scopes, runs []string, ttl time.Duration) (token, jti string, err error) {
	return i.issueAgent(agentID, scopes, runs, "", ttl)
}

// IssueAgentTokenFull mints an agent JWT with scopes, a run whitelist, and a
// tenant (ADR 0007). Empty tenant keeps the token tenant-less.
func (i *Issuer) IssueAgentTokenFull(agentID string, scopes, runs []string, tenant string, ttl time.Duration) (token, jti string, err error) {
	return i.issueAgent(agentID, scopes, runs, tenant, ttl)
}

func (i *Issuer) issueAgent(agentID string, scopes, runs []string, tenant string, ttl time.Duration) (string, string, error) {
	return i.issue(agentID, TokenKindAgent, append([]string(nil), scopes...), append([]string(nil), runs...), tenant, nil, ttl)
}

func (i *Issuer) issue(subject string, kind TokenKind, scopes, runs []string, tenant string, tenants []string, ttl time.Duration) (string, string, error) {
	if subject == "" {
		return "", "", errors.New("subject is required")
	}
	if ttl <= 0 {
		return "", "", errors.New("ttl must be positive")
	}
	jti, err := newJTI()
	if err != nil {
		return "", "", fmt.Errorf("generate jti: %w", err)
	}
	now := time.Now().UTC()
	claims := Claims{
		UserID:  subject,
		Kind:    kind,
		Scopes:  scopes,
		Runs:    runs,
		Tenant:  tenant,
		Tenants: tenants,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Issuer:    "mnemos",
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	// Set kid header so verifiers can pick the right secret during
	// rotation. Tokens without a kid stay compatible (verifier falls
	// back to the Keyring's primary entry).
	tok.Header["kid"] = i.keyring.Primary()
	signed, err := tok.SignedString(i.keyring.PrimarySecret())
	if err != nil {
		return "", "", fmt.Errorf("sign token: %w", err)
	}
	return signed, jti, nil
}

// ParseAndValidate parses tokenString, verifies its signature against
// the keyring-resolved secret, checks expiry, and consults the
// revocation denylist. Returns the validated claims or an error
// explaining why the token was rejected.
//
// Kid resolution:
//   - If the JWT header carries a kid, that kid MUST be present in the
//     keyring; an unknown kid fails fast (no fallback to other secrets,
//     since the issuer told us which key to use).
//   - If the JWT header has no kid (legacy tokens minted before this
//     surface existed), every registered key is tried in iteration
//     order until one signature matches. This preserves the original
//     dual-key fallback semantics for in-flight legacy tokens during
//     the rotation transition.
func (v *Verifier) ParseAndValidate(ctx context.Context, tokenString string) (*Claims, error) {
	parseWith := func(secret []byte) (*jwt.Token, error) {
		return jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (any, error) {
			if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return secret, nil
		}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	}

	// Pick the starting secret from the kid header (if any) and fall
	// back through every other registered kid on signature mismatch.
	// The header is a hint, not a strict gate: tokens minted before
	// the kid surface existed have no header, and tokens minted under
	// a kid label that later gets renamed (or moved between "primary"
	// and "previous" during dual-key rotation) still need to validate
	// as long as one of the keyring's secrets matches the signature.
	// Non-signature errors (expired token, malformed JWT, unknown
	// alg) short-circuit immediately.
	kid := extractKid(tokenString)
	tryOrder := orderedKids(v.keyring, kid)
	var (
		parsed *jwt.Token
		err    error
	)
	for _, k := range tryOrder {
		secret, ok := v.keyring.Secret(k)
		if !ok {
			continue
		}
		parsed, err = parseWith(secret)
		if err == nil {
			break
		}
		if !isSignatureError(err) {
			// Don't keep trying other secrets for non-signature errors;
			// those are token-shape failures that won't recover.
			return nil, fmt.Errorf("parse token: %w", err)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}

	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, errors.New("invalid token claims")
	}
	if claims.UserID == "" {
		return nil, errors.New("token missing subject")
	}

	revoked, err := v.revoked.IsRevoked(ctx, claims.ID)
	if err != nil {
		return nil, fmt.Errorf("check revocation: %w", err)
	}
	if revoked {
		return nil, errors.New("token revoked")
	}

	return claims, nil
}

// isSignatureError reports whether err is a signature-mismatch
// failure (i.e. the only error category for which falling back to
// the previous key is sensible). Other failures — expired token,
// malformed JWT, unknown alg — should not trigger fallback.
func isSignatureError(err error) bool {
	return errors.Is(err, jwt.ErrSignatureInvalid) ||
		errors.Is(err, jwt.ErrTokenSignatureInvalid)
}

// orderedKids returns the kid order [Verifier.ParseAndValidate]
// should try when verifying signatures. The header-supplied kid (if
// recognised by the keyring) goes first; the primary kid is next;
// every other registered kid follows. This makes the new-token path
// (header kid → instant match) O(1) and the legacy / mid-rotation
// fallback path O(N).
func orderedKids(k *Keyring, headerKid string) []string {
	all := k.Kids()
	out := make([]string, 0, len(all)+1)
	seen := make(map[string]struct{}, len(all))
	add := func(kid string) {
		if kid == "" {
			return
		}
		if _, dup := seen[kid]; dup {
			return
		}
		if _, ok := k.Secret(kid); !ok {
			return
		}
		seen[kid] = struct{}{}
		out = append(out, kid)
	}
	add(headerKid)
	add(k.Primary())
	for _, kid := range all {
		add(kid)
	}
	return out
}

// extractKid pulls the `kid` header parameter from a JWT without
// validating the signature. Used by [Verifier.ParseAndValidate] to
// pick the right secret before parse-with-signature runs.
//
// On any decode error (malformed token, missing header, non-string
// kid) returns "" — the caller treats that as the legacy "no kid
// header" path and falls back to the primary secret.
func extractKid(tokenString string) string {
	// jwt.ParseUnverified parses the token without checking the
	// signature, which is exactly what we need to read the header.
	tok, _, err := new(jwt.Parser).ParseUnverified(tokenString, &Claims{})
	if err != nil {
		return ""
	}
	if kid, ok := tok.Header["kid"].(string); ok {
		return kid
	}
	return ""
}

// GenerateSecret returns 32 bytes of cryptographically random data,
// suitable for use as the JWT signing secret. Persist this securely;
// rotating it invalidates every issued token.
func GenerateSecret() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	return b, nil
}

func newJTI() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
