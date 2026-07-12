package main

import (
	"os"
	"path/filepath"
	"testing"
)

// setupCaptureRepo builds an opted-in repo brain under a temp HOME, points the
// global (central) brain elsewhere, seeds one high-trust general learning into
// the repo brain, and returns the repo dir + central DSN. It also writes a
// minimal transcript so hookCapture has something to ingest.
func setupCaptureRepo(t *testing.T) (repo, centralDSN, transcript string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config")) // no real workspaces
	t.Setenv("MNEMOS_URL", "")                                  // not hosted
	t.Setenv("MNEMOS_LLM_PROVIDER", "")                         // rule-based capture

	repo = filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(repo, ".mnemos"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoDSN := "sqlite://" + filepath.Join(repo, ".mnemos", "mnemos.db")

	centralDSN = "sqlite://" + filepath.Join(home, "central.db")
	t.Setenv("MNEMOS_DB_URL", centralDSN)

	// A high-trust, general (non-repo-specific) learning that qualifies to float.
	seedSourceClaim(t, repoDSN, "c_general", generalText, "fact", 0.9, false)

	transcript = filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(transcript,
		[]byte(`{"type":"user","message":{"role":"user","content":"wrap up the session"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo, centralDSN, transcript
}

// TestHookCapture_FloatBackOffIsNoOp is the safety gate: with the opt-in flag
// unset, a session-end capture inside a repo NEVER floats anything up — the
// central brain stays empty, exactly as before the feature existed.
func TestHookCapture_FloatBackOffIsNoOp(t *testing.T) {
	repo, centralDSN, transcript := setupCaptureRepo(t)
	// Flag explicitly off (default).
	t.Setenv("MNEMOS_FLOATBACK_ON_CAPTURE", "")

	hookCapture(hookEvent{Cwd: repo, TranscriptPath: transcript})

	if got := centralTexts(t, centralDSN); len(got) != 0 {
		t.Fatalf("flag off must not float anything up; central has %d claims: %v", len(got), got)
	}
}

// TestHookCapture_FloatBackOnPromotes proves the opt-in works: with the flag on,
// the same capture floats the high-trust general learning up into the central
// brain (best-effort, reusing the float-back apply pipeline).
func TestHookCapture_FloatBackOnPromotes(t *testing.T) {
	repo, centralDSN, transcript := setupCaptureRepo(t)
	t.Setenv("MNEMOS_FLOATBACK_ON_CAPTURE", "true")

	hookCapture(hookEvent{Cwd: repo, TranscriptPath: transcript})

	got := centralTexts(t, centralDSN)
	if !got[generalText] {
		t.Fatalf("flag on should float the high-trust learning up; central=%v", got)
	}
}
