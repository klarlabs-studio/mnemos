package auth

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadOrCreateSecret resolves the JWT signing secret in this order:
//  1. MNEMOS_JWT_SECRET env var (hex-encoded; useful in CI/Docker)
//  2. The file at path, hex-decoded (auto-created with 32 random bytes
//     if it doesn't exist)
//
// Returns the secret bytes plus a bool that is true when the file was
// auto-created — callers can print a warning so the operator knows a
// new secret was generated and that any previously-issued tokens are
// invalidated.
func LoadOrCreateSecret(path string) ([]byte, bool, error) {
	if envHex := strings.TrimSpace(os.Getenv("MNEMOS_JWT_SECRET")); envHex != "" {
		b, err := hex.DecodeString(envHex)
		if err != nil {
			return nil, false, fmt.Errorf("MNEMOS_JWT_SECRET is not valid hex: %w", err)
		}
		if len(b) < 32 {
			return nil, false, errors.New("MNEMOS_JWT_SECRET must decode to at least 32 bytes")
		}
		return b, false, nil
	}

	if path == "" {
		return nil, false, errors.New("no secret path provided and MNEMOS_JWT_SECRET unset")
	}

	if data, err := os.ReadFile(path); err == nil {
		decoded, err := hex.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, false, fmt.Errorf("read %s: not hex-encoded: %w", path, err)
		}
		if len(decoded) < 32 {
			return nil, false, fmt.Errorf("read %s: must decode to at least 32 bytes", path)
		}
		return decoded, false, nil
	}

	// Generate.
	secret, err := GenerateSecret()
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, fmt.Errorf("create secret dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(secret)), 0o600); err != nil {
		return nil, false, fmt.Errorf("write secret to %s: %w", path, err)
	}
	return secret, true, nil
}

// LoadPreviousSecret returns the previous-generation signing secret
// when one is configured, or nil when no rotation is in progress.
//
// Resolution order:
//  1. MNEMOS_JWT_PREV_SECRET env var (hex-encoded).
//  2. <auth-dir>/jwt-secret.previous file alongside the active secret.
//
// Returning nil with a nil error is the steady-state path: there is
// no previous secret, the verifier behaves as a single-key verifier.
func LoadPreviousSecret(activePath string) ([]byte, error) {
	if envHex := strings.TrimSpace(os.Getenv("MNEMOS_JWT_PREV_SECRET")); envHex != "" {
		b, err := hex.DecodeString(envHex)
		if err != nil {
			return nil, fmt.Errorf("MNEMOS_JWT_PREV_SECRET is not valid hex: %w", err)
		}
		if len(b) < 32 {
			return nil, errors.New("MNEMOS_JWT_PREV_SECRET must decode to at least 32 bytes")
		}
		return b, nil
	}
	if activePath == "" {
		return nil, nil
	}
	prevPath := activePath + ".previous"
	data, err := os.ReadFile(prevPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", prevPath, err)
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("read %s: not hex-encoded: %w", prevPath, err)
	}
	if len(decoded) < 32 {
		return nil, fmt.Errorf("read %s: must decode to at least 32 bytes", prevPath)
	}
	return decoded, nil
}

// DefaultSecretPath returns the default location for the JWT secret
// file. Resolution order:
//  1. MNEMOS_AUTH_DIR env var — explicit override; intended for
//     read-only-rootfs containers where the writable volume is not
//     under $HOME (#21).
//  2. .mnemos/jwt-secret inside the project root, when one is found.
//  3. $HOME/.mnemos/jwt-secret as the XDG-equivalent fallback.
//
// Always returns "jwt-secret" as the basename so callers can compose
// the full path without re-discovering it.
func DefaultSecretPath(projectRoot string) string {
	if dir := strings.TrimSpace(os.Getenv("MNEMOS_AUTH_DIR")); dir != "" {
		return filepath.Join(dir, "jwt-secret")
	}
	if projectRoot != "" {
		return filepath.Join(projectRoot, ".mnemos", "jwt-secret")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".mnemos", "jwt-secret")
}
