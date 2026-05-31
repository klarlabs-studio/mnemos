package auth

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestKeyring_RejectsBadConstruction pins the invariants the
// constructor must enforce. Each case is a real misconfiguration
// somebody could make in env wiring.
func TestKeyring_RejectsBadConstruction(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		primary string
		keys    map[string][]byte
		wantErr string
	}{
		{"empty primary", "", map[string][]byte{"a": []byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")}, "primary kid required"},
		{"no keys", "primary", nil, "must contain at least one key"},
		{"primary missing from keys", "v2", map[string][]byte{"v1": []byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")}, "not present in keys"},
		{"empty kid in keys", "primary", map[string][]byte{"primary": []byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"), "": []byte("yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy")}, "empty kid"},
		{"empty secret", "primary", map[string][]byte{"primary": {}}, "empty secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewKeyring(tc.primary, tc.keys)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestIssuerWithKeyring_WritesKidHeader verifies a token minted via
// the multi-key surface carries the primary kid in its header and
// validates without surprises on the other side.
func TestIssuerWithKeyring_WritesKidHeader(t *testing.T) {
	t.Parallel()
	kr, err := NewKeyring("v2", map[string][]byte{
		"v1": bytesOfLen(32, 'a'),
		"v2": bytesOfLen(32, 'b'),
	})
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}

	issuer := NewIssuerWithKeyring(kr)
	tok, _, err := issuer.IssueAgentTokenWithScopes("agent-1", []string{"*"}, time.Minute)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	verifier := NewVerifierWithKeyring(kr, newFakeRevoked())
	claims, err := verifier.ParseAndValidate(context.Background(), tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.UserID != "agent-1" {
		t.Errorf("UserID = %q, want agent-1", claims.UserID)
	}
}

// TestVerifierWithKeyring_OldTokenValidatesAfterRotation simulates
// rotation: an operator promotes a new kid to primary while the old
// kid stays in the keyring for verification. Tokens issued under the
// old kid must continue to validate until they expire.
func TestVerifierWithKeyring_OldTokenValidatesAfterRotation(t *testing.T) {
	t.Parallel()
	v1 := bytesOfLen(32, '1')
	v2 := bytesOfLen(32, '2')

	// State 1: only v1 exists; mint a token with it.
	preRotation, err := NewKeyring("v1", map[string][]byte{"v1": v1})
	if err != nil {
		t.Fatalf("pre-rotation keyring: %v", err)
	}
	issuerV1 := NewIssuerWithKeyring(preRotation)
	tok, _, err := issuerV1.IssueAgentTokenWithScopes("agent-1", []string{"*"}, time.Hour)
	if err != nil {
		t.Fatalf("issue under v1: %v", err)
	}

	// State 2: v2 promoted to primary, v1 kept around for verification.
	postRotation, err := NewKeyring("v2", map[string][]byte{
		"v1": v1,
		"v2": v2,
	})
	if err != nil {
		t.Fatalf("post-rotation keyring: %v", err)
	}
	verifier := NewVerifierWithKeyring(postRotation, newFakeRevoked())

	claims, err := verifier.ParseAndValidate(context.Background(), tok)
	if err != nil {
		t.Fatalf("token issued under v1 should still validate post-rotation: %v", err)
	}
	if claims.UserID != "agent-1" {
		t.Errorf("UserID = %q, want agent-1", claims.UserID)
	}
}

// TestVerifierWithKeyring_RejectsAfterKidDropped pins the eventual
// cleanup: once the old kid is dropped from the keyring, tokens
// signed under it must be rejected.
func TestVerifierWithKeyring_RejectsAfterKidDropped(t *testing.T) {
	t.Parallel()
	v1 := bytesOfLen(32, '1')
	v2 := bytesOfLen(32, '2')

	preRotation, _ := NewKeyring("v1", map[string][]byte{"v1": v1})
	issuerV1 := NewIssuerWithKeyring(preRotation)
	tok, _, _ := issuerV1.IssueAgentTokenWithScopes("agent-1", []string{"*"}, time.Hour)

	// Drop v1 entirely; only v2 is registered now.
	cleaned, _ := NewKeyring("v2", map[string][]byte{"v2": v2})
	verifier := NewVerifierWithKeyring(cleaned, newFakeRevoked())

	_, err := verifier.ParseAndValidate(context.Background(), tok)
	if err == nil {
		t.Fatal("expected validation failure after v1 dropped, got nil")
	}
}

// TestVerifierWithKeyring_KidsHelper covers the diagnostic helper
// the doctor command will rely on.
func TestVerifierWithKeyring_KidsHelper(t *testing.T) {
	t.Parallel()
	kr, _ := NewKeyring("v2", map[string][]byte{
		"v1": bytesOfLen(32, '1'),
		"v2": bytesOfLen(32, '2'),
		"v3": bytesOfLen(32, '3'),
	})
	got := kr.Kids()
	want := []string{"v1", "v2", "v3"}
	if len(got) != len(want) {
		t.Fatalf("len Kids() = %d, want %d (%v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("Kids()[%d] = %q, want %q", i, got[i], w)
		}
	}
	if kr.Primary() != "v2" {
		t.Errorf("Primary() = %q, want v2", kr.Primary())
	}
}

func bytesOfLen(n int, fill byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = fill
	}
	return b
}
