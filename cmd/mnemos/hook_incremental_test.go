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
	t.Setenv("MNEMOS_CAPTURE_STRATEGY", "incremental")
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

// The capture strategy decides how much work happens when. It matters because
// the right answer differs by provider: a slow local model can only manage
// small continuous chunks, while a fast cloud model is better served by one
// request at session end (N per-turn calls each re-send the ~5KB extraction
// prompt, so continuous capture costs a paid API materially more).
func TestCaptureStrategyResolution(t *testing.T) {
	cases := []struct {
		name     string
		strategy string
		provider string
		baseURL  string
		want     string
	}{
		// Explicit always wins.
		{"explicit incremental", "incremental", "anthropic", "", captureIncremental},
		{"explicit end", "end", "ollama", "http://localhost:11434", captureAtEnd},
		{"explicit off", "off", "ollama", "", captureOff},
		{"case insensitive", "  INCREMENTAL ", "anthropic", "", captureIncremental},

		// auto, settled by provider.
		{"auto ollama", "", "ollama", "", captureIncremental},
		{"auto anthropic", "", "anthropic", "", captureAtEnd},
		{"auto openai", "", "openai", "https://api.openai.com/v1", captureAtEnd},
		{"auto no provider", "", "", "", captureAtEnd},

		// A hosted provider stays hosted even behind a local gateway. Keying on
		// a loopback URL got this wrong and would bill an API call per turn.
		{"anthropic via localhost proxy", "", "anthropic", "http://localhost:4000", captureAtEnd},

		// Unrecognised values must not silently disable capture.
		{"garbage falls back to auto", "sometimes", "ollama", "", captureIncremental},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MNEMOS_CAPTURE_STRATEGY", tc.strategy)
			t.Setenv("MNEMOS_LLM_PROVIDER", tc.provider)
			t.Setenv("MNEMOS_LLM_BASE_URL", tc.baseURL)
			if got := captureStrategy(); got != tc.want {
				t.Errorf("captureStrategy() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Installing Stop/PreCompact for a cloud user would add an API call per turn.
// The hook set must follow the strategy.
func TestHookSpecsFollowCaptureStrategy(t *testing.T) {
	hasIncremental := func(specs []hookSpec) bool {
		for _, s := range specs {
			if s.Sub == "capture-incremental" {
				return true
			}
		}
		return false
	}
	hasSessionEnd := func(specs []hookSpec) bool {
		for _, s := range specs {
			if s.Event == "SessionEnd" {
				return true
			}
		}
		return false
	}

	t.Setenv("MNEMOS_CAPTURE_STRATEGY", "incremental")
	specs := hookSpecsFor(map[string]bool{"capture": true})
	if !hasIncremental(specs) || !hasSessionEnd(specs) {
		t.Error("incremental strategy needs Stop/PreCompact plus the SessionEnd sweep")
	}

	t.Setenv("MNEMOS_CAPTURE_STRATEGY", "end")
	specs = hookSpecsFor(map[string]bool{"capture": true})
	if hasIncremental(specs) {
		t.Error("end strategy must not install per-turn hooks; that is an API call per turn")
	}
	if !hasSessionEnd(specs) {
		t.Error("end strategy still needs the SessionEnd capture")
	}

	t.Setenv("MNEMOS_CAPTURE_STRATEGY", "off")
	specs = hookSpecsFor(map[string]bool{"capture": true})
	if len(specs) != 0 {
		t.Errorf("off strategy installed %d capture hook(s), want none", len(specs))
	}
}

// A strategy change must take effect without re-running `mnemos init`: hooks
// already installed have to respect the current config at runtime.
func TestIncrementalHookNoOpsWhenStrategyIsEnd(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("MNEMOS_CAPTURE_STRATEGY", "end")

	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"a durable decision was recorded"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ev := hookEvent{SessionID: "s1", TranscriptPath: path}

	// Worker mode so no process is spawned; it must decline to do the work.
	hookCaptureIncremental(ev, true, nil)

	if off := loadCaptureOffset("s1"); off != 0 {
		t.Errorf("incremental capture ran under strategy=end (offset advanced to %d)", off)
	}
}

// Configuration cannot tell you how fast extraction will be, so `auto`
// resolves only the case it actually knows and defaults the rest to `end`.
//
// The aggregators are why: OpenRouter, Together, Fireworks, Groq and DeepInfra
// all serve open-weight models fast over an OpenAI-compatible API. "llama"
// under ollama is slow; the same name under OpenRouter is fast. Any heuristic
// over provider or model name gets one of them wrong, and gets wrong again
// with each new service.
func TestAutoStrategyDoesNotGuessBeyondOllama(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		baseURL  string
		model    string
		want     string
	}{
		{"ollama is definitionally local", "ollama", "", "qwen2.5:14b", captureIncremental},

		// Aggregators: open-weight names, hosted speed. Must not be incremental.
		{"openrouter llama", "openai-compat", "https://openrouter.ai/api/v1", "meta-llama/llama-3.3-70b-instruct", captureAtEnd},
		{"openrouter claude", "openai-compat", "https://openrouter.ai/api/v1", "anthropic/claude-sonnet-4", captureAtEnd},
		{"together mixtral", "openai-compat", "https://api.together.xyz/v1", "mistralai/Mixtral-8x7B", captureAtEnd},
		{"groq llama", "openai-compat", "https://api.groq.com/openai/v1", "llama-3.3-70b-versatile", captureAtEnd},

		// Hosted providers stay hosted behind a local gateway.
		{"anthropic via localhost proxy", "anthropic", "http://localhost:4000", "claude-sonnet-4", captureAtEnd},

		// A slow local runtime behind openai-compat defaults to end; the user
		// sets capture.strategy explicitly. Safe, not correct — and that is the
		// deliberate trade, since the opposite error costs money silently.
		{"lm studio local", "openai-compat", "http://localhost:1234/v1", "qwen2.5-14b-instruct", captureAtEnd},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MNEMOS_CAPTURE_STRATEGY", "")
			t.Setenv("MNEMOS_LLM_PROVIDER", tc.provider)
			t.Setenv("MNEMOS_LLM_BASE_URL", tc.baseURL)
			t.Setenv("MNEMOS_LLM_MODEL", tc.model)
			if got := captureStrategy(); got != tc.want {
				t.Errorf("%s -> %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// The strategy is a question for the user, not something to infer. init asks
// and records the answer; --capture answers it non-interactively; and with no
// one to ask, nothing is written so the documented default applies rather than
// a guess being baked into the user's config.
func TestResolveCaptureStrategyFromInitOptions(t *testing.T) {
	cases := []struct {
		name string
		opts initOptions
		want string
	}{
		{"explicit flag", initOptions{hooks: defaultHookSet(), capture: "incremental"}, captureIncremental},
		{"explicit flag end", initOptions{hooks: defaultHookSet(), capture: "end"}, captureAtEnd},
		{"flag is case-insensitive", initOptions{hooks: defaultHookSet(), capture: " END "}, captureAtEnd},
		{"--yes writes nothing", initOptions{hooks: defaultHookSet(), yes: true}, ""},
		{"--dry-run writes nothing", initOptions{hooks: defaultHookSet(), dryRun: true}, ""},
		{"no capture hook, nothing to choose", initOptions{hooks: map[string]bool{"recall": true}}, ""},
		{"--no-hooks", initOptions{hooks: defaultHookSet(), noHooks: true}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveCaptureStrategy(tc.opts); got != tc.want {
				t.Errorf("resolveCaptureStrategy = %q, want %q", got, tc.want)
			}
		})
	}
}

// In hosted mode the server does the extraction, so the local provider says
// nothing about how long it takes. Keying on it would flip a hosted client to
// per-turn capture just because ollama happens to be installed for something
// else — one request per turn to the server, where one per session would do.
func TestAutoStrategyIgnoresLocalProviderWhenHosted(t *testing.T) {
	t.Setenv("MNEMOS_CAPTURE_STRATEGY", "")
	t.Setenv("MNEMOS_LLM_PROVIDER", "ollama")
	t.Setenv("MNEMOS_URL", "https://brain.example.com")

	if got := captureStrategy(); got != captureAtEnd {
		t.Errorf("hosted brain with local ollama -> %q, want %q "+
			"(extraction runs server-side; the local provider is irrelevant)", got, captureAtEnd)
	}

	// An explicit choice still wins — a self-hosted server on slow hardware is
	// a real case, and only the operator knows.
	t.Setenv("MNEMOS_CAPTURE_STRATEGY", "incremental")
	if got := captureStrategy(); got != captureIncremental {
		t.Errorf("explicit strategy overridden in hosted mode: %q", got)
	}
}
