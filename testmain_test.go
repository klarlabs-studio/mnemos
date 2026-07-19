package mnemos_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain points the library test binary at a throwaway brain.
//
// Every test here currently passes explicit storage (WithStorage / WithSQLite),
// so nothing is known to leak today. This is a guard rather than a fix: when
// MNEMOS_DB_URL is unset, mnemos.New falls back to
// ~/.local/share/mnemos/mnemos.db — the developer's real brain — so a single
// future test that forgets a storage option would silently read and write
// production data. That exact omission in cmd/mnemos wrote test fixtures into
// a live 118 MB brain and produced an intermittent -race CI failure; see the
// TestMain there for the full account.
//
// clearMnemosEnv() unsets MNEMOS_DB_URL to exercise provider defaults, which is
// precisely the state that would fall through, so the guard is cheap insurance.
func TestMain(m *testing.M) {
	var brainDir string
	if os.Getenv("MNEMOS_DB_URL") == "" {
		d, err := os.MkdirTemp("", "mnemos-lib-test-brain-")
		if err != nil {
			fmt.Fprintf(os.Stderr, "testmain: create isolated brain dir: %v\n", err)
			os.Exit(1)
		}
		brainDir = d
		_ = os.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(d, "mnemos.db"))
	}

	code := m.Run()

	// os.Exit skips defers, so clean up explicitly.
	if brainDir != "" {
		_ = os.RemoveAll(brainDir)
	}
	os.Exit(code)
}
