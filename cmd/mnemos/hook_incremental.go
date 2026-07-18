package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Incremental capture (ADR-adjacent to the SessionEnd capture in hook.go).
//
// One 20KiB capture at SessionEnd is too much work for a slow local model: the
// whole transcript in one extraction request overruns the per-request LLM
// timeout, so the run degrades to rule-based. Individual exchanges, by
// contrast, are tiny — median ~136 chars in a real transcript — and extract in
// seconds.
//
// So capture continuously instead of all at once: the Stop hook fires after
// each assistant response and PreCompact fires before context is summarised
// away, each ingesting only the transcript lines written since the last run.
// SessionEnd then sweeps whatever remains, which is usually nothing.
//
// These run inside the user's editor loop, so the work is detached: the hook
// process spawns a worker and exits immediately, and the user never waits on
// extraction. Everything here is fail-open — a missing offset, an unreadable
// transcript, or a dead worker costs at most one chunk of knowledge, never the
// session.

// captureStateDir holds one offset file per session, under the same XDG data
// dir as the global brain so state travels with the installation.
func captureStateDir() string {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join("data", "capture-state")
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "mnemos", "capture-state")
}

// captureStateFile maps a session id to its offset file. The id comes from the
// hook payload, so it is hashed rather than trusted as a path component: a
// session id containing separators must not write outside the state dir.
func captureStateFile(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return filepath.Join(captureStateDir(), hex.EncodeToString(sum[:16])+".offset")
}

// loadCaptureOffset returns how far into the transcript this session has
// already been captured. Unknown sessions and unreadable state start at 0,
// which re-captures rather than silently skipping.
func loadCaptureOffset(sessionID string) int64 {
	if strings.TrimSpace(sessionID) == "" {
		return 0
	}
	data, err := os.ReadFile(captureStateFile(sessionID)) //nolint:gosec // path is hash-derived
	if err != nil {
		return 0
	}
	off, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || off < 0 {
		return 0
	}
	return off
}

// storeCaptureOffset records the new high-water mark. Written only after the
// pipeline has persisted the chunk, so a failure re-captures rather than
// dropping knowledge.
func storeCaptureOffset(sessionID string, off int64) error {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	if err := os.MkdirAll(captureStateDir(), 0o750); err != nil {
		return err
	}
	return os.WriteFile(captureStateFile(sessionID), []byte(strconv.FormatInt(off, 10)), 0o600)
}

// extractTranscriptTextFrom pulls conversational text written at or after
// offset, returning the text and the new end-of-file offset to record.
//
// An offset past EOF means the transcript was truncated or replaced, so it
// restarts from 0: re-capturing is idempotent enough (content-addressed
// downstream) and is far better than going permanently blind on a session.
func extractTranscriptTextFrom(path string, offset int64, maxBytes int) (string, int64) {
	f, err := os.Open(path) //nolint:gosec // path supplied by Claude Code hook payload
	if err != nil {
		return "", offset
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return "", offset
	}
	size := info.Size()
	if offset > size {
		offset = 0
	}
	if offset == size {
		return "", offset
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", offset
	}

	data, err := io.ReadAll(io.LimitReader(f, 4<<20))
	if err != nil {
		return "", offset
	}
	// A partial trailing line (the writer may be mid-append) is left for the
	// next run: rewind the offset to the last newline so nothing is lost or
	// half-parsed.
	consumed := len(data)
	if idx := strings.LastIndexByte(string(data), '\n'); idx >= 0 {
		consumed = idx + 1
		data = data[:consumed]
	}

	return transcriptTextFromLines(string(data), maxBytes), offset + int64(consumed)
}

// spawnDetachedCapture re-execs this binary as a background worker for the
// given hook sub-command and hands it the event JSON on stdin, then returns
// without waiting. The user's editor loop never blocks on extraction.
func spawnDetachedCapture(sub string, payload []byte) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"hook", sub, "--worker"}
	if dsn := strings.TrimSpace(os.Getenv("MNEMOS_DB_URL")); dsn != "" {
		args = append(args, "--db", dsn)
	}
	cmd := exec.Command(self, args...) //nolint:gosec // self-exec with fixed args

	// Hand the payload over as a real file descriptor, not an io.Reader.
	// os/exec services a non-*os.File Stdin with a copier goroutine in THIS
	// process — and the hook calls os.Exit as soon as Start returns, killing
	// that goroutine before it writes. The child would then read empty stdin,
	// decode a zero event, and silently do nothing. A *os.File is passed
	// straight to the child as fd 0, with no copier involved.
	tmp, err := os.CreateTemp("", "mnemos-hook-*.json")
	if err != nil {
		return err
	}
	// Unlink now: on Unix the child keeps the open descriptor, so the payload
	// survives while leaving nothing behind if we die before it runs.
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		return err
	}
	defer func() { _ = tmp.Close() }()
	cmd.Stdin = tmp
	// Detach: new session so the worker outlives the hook process and is not
	// killed when Claude Code reaps the hook's process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	// Release so the child is not left a zombie when this process exits.
	return cmd.Process.Release()
}

// hookCaptureIncremental handles Stop and PreCompact. Unless it is the worker,
// it spawns one and returns immediately.
func hookCaptureIncremental(ev hookEvent, isWorker bool, raw []byte) {
	if strings.TrimSpace(ev.TranscriptPath) == "" {
		return
	}
	// Honor the strategy at runtime, not just at install time: changing
	// capture.strategy should take effect without re-running `mnemos init`,
	// and the hooks may already be installed from an earlier choice.
	if captureStrategy() != captureIncremental {
		return
	}
	if !isWorker {
		// Best-effort: if spawning fails there is simply no incremental capture
		// this turn, and SessionEnd still sweeps the remainder.
		_ = spawnDetachedCapture("capture-incremental", raw)
		return
	}
	captureRange(ev, incrementalChunkBytes)
}

// incrementalChunkBytes caps one incremental chunk. Exchanges are small
// (median ~136 chars), so this bounds a burst — a long tool-heavy turn, or the
// backlog PreCompact sees — rather than a typical turn.
const incrementalChunkBytes = 8 * 1024

// captureRange captures the not-yet-seen span of the session's transcript and
// advances the offset only after the pipeline reports success.
func captureRange(ev hookEvent, maxBytes int) {
	offset := loadCaptureOffset(ev.SessionID)
	text, newOffset := extractTranscriptTextFrom(ev.TranscriptPath, offset, maxBytes)
	if strings.TrimSpace(text) == "" {
		return
	}
	if !captureText(ev, text) {
		return // leave the offset alone so the next run retries this span
	}
	if err := storeCaptureOffset(ev.SessionID, newOffset); err != nil {
		fmt.Fprintf(os.Stderr, "mnemos: capture offset not saved: %v\n", err)
	}
}

// Capture strategies. Which one is right depends on the provider, and the
// difference is not cosmetic:
//
//   - A slow local model cannot extract a whole transcript in one request at
//     all (measured: ~23 minutes for 20KiB on qwen2.5:14b), so it needs the
//     work spread across the session.
//   - A fast hosted model handles a whole transcript in one request, and
//     capturing per turn would instead mean an API call per turn — each
//     re-sending the ~5KB extraction prompt. That is real money for no gain.
const (
	// captureIncremental captures during the session (Stop + PreCompact), with
	// SessionEnd sweeping the remainder.
	captureIncremental = "incremental"
	// captureAtEnd captures once at SessionEnd. The original behavior.
	captureAtEnd = "end"
	// captureOff installs no capture hooks at all.
	captureOff = "off"
	// "auto" is also accepted (and is the default): it resolves to one of the
	// above from the configured provider, so it is never itself a result.
)

// captureStrategy resolves MNEMOS_CAPTURE_STRATEGY (`capture.strategy` in
// mnemos.yaml). Unset or unrecognised means auto, which never disables
// capture: a typo should not silently stop recording knowledge.
func captureStrategy() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MNEMOS_CAPTURE_STRATEGY"))) {
	case captureIncremental:
		return captureIncremental
	case captureAtEnd:
		return captureAtEnd
	case captureOff:
		return captureOff
	}
	return autoStrategy()
}

// autoStrategy guesses a strategy from the configured provider and model.
//
// The axis that matters is extraction LATENCY, not where the socket points. An
// earlier version keyed on a loopback base URL, which gets the common cases
// right and the interesting one wrong: a hosted model behind a local gateway
// (Sonnet via a proxy on localhost) is a loopback URL with cloud speed, and
// would have been billed per turn for no reason.
//
// So: decide on the provider where the provider settles it, fall back to the
// model name where it does not, and prefer `end` when genuinely unsure —
// guessing `end` wrongly degrades extraction on a slow model (visible, and
// fixable by setting the strategy), while guessing `incremental` wrongly
// spends real money per turn.
func autoStrategy() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MNEMOS_LLM_PROVIDER"))) {
	case "ollama":
		// Always inference on this machine.
		return captureIncremental
	case "anthropic", "openai", "gemini":
		// Always hosted, regardless of any base-URL override.
		return captureAtEnd
	case "":
		// No LLM: rule-based extraction is instant, so one pass at the end.
		return captureAtEnd
	}
	// openai-compat is genuinely ambiguous — it fronts LM Studio, llama.cpp and
	// vLLM as readily as it fronts a cloud gateway. The model name is the best
	// remaining signal: open-weight names indicate local inference.
	return strategyForModel(os.Getenv("MNEMOS_LLM_MODEL"))
}

// openWeightModels are name fragments of models that are run locally. Matching
// on names is a heuristic and will age; it only ever selects between two
// working modes, and capture.strategy overrides it outright.
var openWeightModels = []string{
	"llama", "qwen", "mistral", "mixtral", "gemma", "phi",
	"deepseek", "codestral", "starcoder", "granite", "olmo", "smollm",
}

// strategyForModel infers from the model name, defaulting to `end` for
// anything unrecognised.
func strategyForModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return captureAtEnd
	}
	for _, frag := range openWeightModels {
		if strings.Contains(m, frag) {
			return captureIncremental
		}
	}
	return captureAtEnd
}
