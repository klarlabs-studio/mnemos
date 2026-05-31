package auth

import (
	"errors"
	"fmt"
	"sort"
)

// Keyring holds N JWT signing secrets keyed by `kid` (RFC 7515 §4.1.4
// "key id" header parameter). The Primary kid is the one new tokens
// sign with; the rest stay around for verification only, letting an
// operator rotate without invalidating any outstanding token in
// flight.
//
// This generalises the previous dual-key (`active` + `previous`)
// rotation surface: a Keyring can hold any number of overlapping keys
// during a rotation window, then drop expired kids once their
// longest-lived issued token has expired.
//
// Backwards compatibility:
//   - Tokens minted without a `kid` header continue to verify against
//     the keyring's Primary entry (the legacy single-secret behaviour).
//   - The legacy [NewIssuer] / [NewVerifier] / [NewVerifierWithPrevious]
//     constructors now wrap a Keyring internally and keep working
//     unchanged.
type Keyring struct {
	primary string
	keys    map[string][]byte
}

// NewKeyring constructs a Keyring from a kid→secret map. The
// `primary` kid must be present in `keys`; every secret must be
// non-empty.
func NewKeyring(primary string, keys map[string][]byte) (*Keyring, error) {
	if primary == "" {
		return nil, errors.New("auth: keyring primary kid required")
	}
	if len(keys) == 0 {
		return nil, errors.New("auth: keyring must contain at least one key")
	}
	if _, ok := keys[primary]; !ok {
		return nil, fmt.Errorf("auth: primary kid %q not present in keys", primary)
	}
	// Defensive copy + validation.
	cp := make(map[string][]byte, len(keys))
	for kid, secret := range keys {
		if kid == "" {
			return nil, errors.New("auth: empty kid in keyring")
		}
		if len(secret) == 0 {
			return nil, fmt.Errorf("auth: empty secret for kid %q", kid)
		}
		cp[kid] = append([]byte(nil), secret...)
	}
	return &Keyring{primary: primary, keys: cp}, nil
}

// Primary returns the kid new tokens sign with.
func (k *Keyring) Primary() string { return k.primary }

// PrimarySecret returns the signing secret for the primary kid.
func (k *Keyring) PrimarySecret() []byte {
	return k.keys[k.primary]
}

// Secret returns the secret for the given kid (or nil + false if the
// kid isn't registered). The legacy single-secret verifier behaviour
// of accepting unsigned-header tokens routes through this with kid=""
// — see [Keyring.LookupForVerify].
func (k *Keyring) Secret(kid string) ([]byte, bool) {
	s, ok := k.keys[kid]
	return s, ok
}

// LookupForVerify resolves a kid extracted from a JWT header to the
// secret a Verifier should use. Empty kid maps to the primary secret
// to preserve compatibility with tokens minted before the kid header
// was added.
func (k *Keyring) LookupForVerify(kid string) ([]byte, bool) {
	if kid == "" {
		return k.keys[k.primary], true
	}
	s, ok := k.keys[kid]
	return s, ok
}

// Kids returns the registered kids in sorted order. Used by tests +
// the doctor command for visibility.
func (k *Keyring) Kids() []string {
	out := make([]string, 0, len(k.keys))
	for kid := range k.keys {
		out = append(out, kid)
	}
	sort.Strings(out)
	return out
}

// newSingleKeyKeyring wraps a single legacy secret as a Keyring with
// the conventional "primary" kid. Used by the back-compat
// constructors [NewIssuer], [NewVerifier], [NewVerifierWithPrevious]
// so existing callers don't have to think about kids.
func newSingleKeyKeyring(secret []byte) *Keyring {
	return &Keyring{
		primary: "primary",
		keys:    map[string][]byte{"primary": append([]byte(nil), secret...)},
	}
}

// newDualKeyKeyring wraps the legacy (active, previous) rotation
// surface as a Keyring. The active secret becomes "primary"; the
// previous secret keeps verifying under kid "previous" until the
// caller drops it.
func newDualKeyKeyring(active, previous []byte) *Keyring {
	keys := map[string][]byte{
		"primary": append([]byte(nil), active...),
	}
	if len(previous) > 0 {
		keys["previous"] = append([]byte(nil), previous...)
	}
	return &Keyring{primary: "primary", keys: keys}
}
