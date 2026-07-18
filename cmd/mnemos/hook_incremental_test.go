package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Incremental capture must ingest each transcript line exactly once. Without a
// per-session offset, a Stop hook firing after every response would re-ingest
// the whole conversation each time, and SessionEnd would duplicate all of it
// again.
func TestExtractTranscriptTextFromOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	line := func(s string) string {
		return `{"type":"user","message":{"role":"user","content":"` + s + `"}}` + "\n"
	}
	if err := os.WriteFile(path, []byte(line("first turn")+line("second turn")), 0o600); err != nil {
		t.Fatal(err)
	}

	text, off := extractTranscriptTextFrom(path, 0, 20*1024)
	if !strings.Contains(text, "first turn") || !strings.Contains(text, "second turn") {
		t.Fatalf("first read missed content: %q", text)
	}
	if off == 0 {
		t.Fatal("offset did not advance")
	}

	// Nothing new yet: a Stop hook firing again must find nothing to do.
	text2, off2 := extractTranscriptTextFrom(path, off, 20*1024)
	if strings.TrimSpace(text2) != "" {
		t.Errorf("re-read returned already-captured content: %q", text2)
	}
	if off2 != off {
		t.Errorf("offset moved with no new data: %d -> %d", off, off2)
	}

	// Append a turn; only the new one comes back.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(line("third turn"))
	_ = f.Close()

	text3, off3 := extractTranscriptTextFrom(path, off, 20*1024)
	if !strings.Contains(text3, "third turn") {
		t.Errorf("new content not returned: %q", text3)
	}
	if strings.Contains(text3, "first turn") || strings.Contains(text3, "second turn") {
		t.Errorf("already-captured content re-returned: %q", text3)
	}
	if off3 <= off {
		t.Errorf("offset did not advance past new data: %d -> %d", off, off3)
	}
}

// A truncated or replaced transcript (offset past EOF) must not silently skip
// everything; it restarts rather than going blind.
func TestExtractTranscriptTextFromOffsetPastEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"only turn"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	text, off := extractTranscriptTextFrom(path, 999999, 20*1024)
	if !strings.Contains(text, "only turn") {
		t.Errorf("offset past EOF should restart from 0, got %q", text)
	}
	if off != int64(len(body)) {
		t.Errorf("offset = %d, want %d", off, len(body))
	}
}

// Offsets persist per session so a later hook run resumes where the last one
// stopped, across processes.
func TestCaptureOffsetRoundTrip(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if got := loadCaptureOffset("sess-1"); got != 0 {
		t.Errorf("unknown session should start at 0, got %d", got)
	}
	if err := storeCaptureOffset("sess-1", 4096); err != nil {
		t.Fatalf("store: %v", err)
	}
	if got := loadCaptureOffset("sess-1"); got != 4096 {
		t.Errorf("offset = %d, want 4096", got)
	}
	// Sessions must not share state.
	if got := loadCaptureOffset("sess-2"); got != 0 {
		t.Errorf("session bleed: sess-2 = %d", got)
	}
	// A session id with path separators must not escape the state dir.
	if err := storeCaptureOffset("../../etc/passwd", 1); err != nil {
		t.Fatalf("store with hostile id: %v", err)
	}
	if got := loadCaptureOffset("../../etc/passwd"); got != 1 {
		t.Errorf("sanitized id round-trip failed: %d", got)
	}
}

// Incremental capture must be installed on both Stop and PreCompact, and its
// timeout must stay small: these fire after every assistant response and only
// spawn a detached worker, so a large timeout would let a hung fork stall the
// user's editor loop.
func TestIncrementalHooksInstalled(t *testing.T) {
	specs := hookSpecsFor(map[string]bool{"capture": true})
	got := map[string]int{}
	for _, s := range specs {
		if s.Sub == "capture-incremental" {
			got[s.Event] = s.Timeout
		}
	}
	for _, ev := range []string{"Stop", "PreCompact"} {
		to, ok := got[ev]
		if !ok {
			t.Errorf("no incremental capture hook installed on %s", ev)
			continue
		}
		if to <= 0 || to > 15 {
			t.Errorf("%s timeout = %ds; it only forks a worker and must not be user-perceptible", ev, to)
		}
	}
	// Ours must still be recognised for idempotent re-install.
	if !isMnemosHookCommand(buildHookCommand("/bin/mnemos", "sqlite:///tmp/a.db", "capture-incremental", true)) {
		t.Error("capture-incremental command not recognised as ours; re-running init would duplicate it")
	}
}
