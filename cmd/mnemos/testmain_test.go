package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates the test binary from the developer's real environment.
//
// Two things are pinned:
//
//  1. A deterministic JWT signing secret. Without it, each test that calls
//     newServerMux would fall back to auth.LoadOrCreateSecret's file path and
//     write `jwt-secret` into the developer's ~/.mnemos directory.
//
//  2. The brain DSN. This is the same class of bug and was considerably worse:
//     resolveDSN() falls back to ~/.local/share/mnemos/mnemos.db when
//     MNEMOS_DB_URL is unset, so every test that let the kernel open a store
//     lazily — rather than passing the temp store openTestStore() hands it —
//     read and WROTE the developer's live brain. Measured: a single run of
//     TestKernel_ProcessTextEvidenceChainIntact added 2 claims to a real
//     118 MB, 10,573-claim brain.
//
//     It also explains the intermittent `-race` CI failure. PersistArtifacts
//     calls RecomputeTrust on every write, which scans every claim and issues
//     one UPDATE per claim; against a brain that grows with each test run
//     that eventually blew the kernel's 5-minute budget under the slowdown of
//     -race plus parallel load, surfacing as
//     "recompute trust: list trust inputs: context deadline exceeded".
//
// Tests that specifically exercise DSN resolution still override with
// t.Setenv("MNEMOS_DB_URL", "") and point HOME / XDG_DATA_HOME at temp dirs,
// so they keep testing the fallback without touching the real brain.
func TestMain(m *testing.M) {
	if os.Getenv("MNEMOS_JWT_SECRET") == "" {
		secret := make([]byte, 32)
		for i := range secret {
			secret[i] = byte(i)
		}
		_ = os.Setenv("MNEMOS_JWT_SECRET", hex.EncodeToString(secret))
	}

	var brainDir string
	if os.Getenv("MNEMOS_DB_URL") == "" {
		d, err := os.MkdirTemp("", "mnemos-test-brain-")
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
