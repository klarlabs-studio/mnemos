package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/client"
)

// Claude Code hook handlers: `mnemos hook <recall|brief|capture>`.
//
// Each reads the hook event JSON from stdin and, for recall/brief, writes a
// JSON payload with hookSpecificOutput.additionalContext to stdout so Claude
// Code injects Mnemos knowledge into the model's context. capture is a
// side-effect-only SessionEnd handler that records the session into the brain.
//
// These run inside the user's editor loop, so they are FAIL-OPEN: any error
// (no brain yet, DB locked, bad stdin) results in exit 0 with no injected
// context. A hook must never block the user or crash their session — an
// unavailable brain simply means "no memory this turn".

// hookEvent is the subset of the Claude Code hook stdin payload we read.
// Fields not sent for a given event stay zero.
type hookEvent struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	Prompt         string `json:"prompt"` // UserPromptSubmit
	Source         string `json:"source"` // SessionStart: startup|resume|clear|compact
	Reason         string `json:"reason"` // SessionEnd
}

// hookContextOutput injects additional context back into Claude Code.
type hookContextOutput struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

type hookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

// handleHook dispatches `mnemos hook <name>`. It always exits 0; failures
// degrade to "no context injected" rather than surfacing to the user.
func handleHook(args []string) {
	// The brain is pinned via the global --db flag (parsed in ParseFlags and
	// exported as MNEMOS_DB_URL before dispatch), so the hook always targets
	// the store the integration was set up against, regardless of the project
	// directory Claude Code launched it from. Here we only need the sub-name.
	name := ""
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			name = a
			break
		}
	}

	ev := readHookEvent(os.Stdin)

	switch name {
	case "recall", "prompt-submitted":
		hookRecall(ev)
	case "brief", "session-start":
		hookBrief(ev)
	case "capture", "session-end":
		hookCapture(ev)
	default:
		// Unknown hook name: nothing to do, but don't fail the session.
	}
	os.Exit(int(ExitSuccess))
}

// readHookEvent decodes the hook payload, tolerating an empty or malformed
// stdin (returns a zero event).
func readHookEvent(r io.Reader) hookEvent {
	var ev hookEvent
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil || len(data) == 0 {
		return ev
	}
	_ = json.Unmarshal(data, &ev)
	return ev
}

// emitContext writes the additionalContext injection payload. Empty context
// emits nothing (a bare exit 0), which Claude Code treats as "no output".
func emitContext(eventName, context string) {
	context = strings.TrimSpace(context)
	if context == "" {
		return
	}
	out := hookContextOutput{HookSpecificOutput: hookSpecificOutput{
		HookEventName:     eventName,
		AdditionalContext: context,
	}}
	_ = json.NewEncoder(os.Stdout).Encode(out)
}

// hostedConfigured reports whether this integration targets a remote hosted
// brain (init --url) rather than a local store. When true, the hooks call the
// REST API via the client package instead of opening a local store.
func hostedConfigured() bool {
	return strings.TrimSpace(os.Getenv(client.EnvBaseURL)) != ""
}

// hostedClient builds a REST client for the configured hosted brain with the
// given per-hook timeout. Only call when hostedConfigured() is true.
func hostedClient(timeout time.Duration) *client.Client {
	opts := []client.Option{client.WithTimeout(timeout)}
	if tok := strings.TrimSpace(os.Getenv("MNEMOS_TOKEN")); tok != "" {
		opts = append(opts, client.WithToken(tok))
	}
	return client.New(os.Getenv(client.EnvBaseURL), opts...)
}

// recallClaim is the transport-agnostic slice of a retrieved claim that the
// recall injection needs, so the local (mcpQueryOutput) and hosted
// (client.SearchResponse) paths share one renderer.
type recallClaim struct {
	Type       string
	Text       string
	TrustScore float64
	Source     string // "global" | "workspace" (blank = untagged)
}

// repoBrain resolves the opt-in repo brain for a session working directory: the
// <repo>/.mnemos/mnemos.db of the nearest ancestor of cwd that has a .mnemos/
// (created by `mnemos init --project`), plus the repo root. Returns ("","") when
// the session isn't inside a repo that opted in — the hooks then use the global
// brain only. Hosted mode has no local repo overlay, and a repo db equal to the
// active (global) brain is not an overlay, so both yield ("",""). Returning the
// root too lets callers avoid re-walking the filesystem.
func repoBrain(cwd string) (dsn, repoRoot string) {
	if hostedConfigured() || strings.TrimSpace(cwd) == "" {
		return "", ""
	}
	// A named workspace (registry, ADR 0010) that owns cwd takes precedence over
	// the implicit .mnemos walk-up. Its matched folder is the AGENTS.md root.
	if wsDSN, _, folder := resolveWorkspaceBrain(cwd); wsDSN != "" {
		if wsDSN == strings.TrimSpace(os.Getenv("MNEMOS_DB_URL")) {
			return "", "" // workspace brain IS the pinned global brain; no overlay
		}
		return wsDSN, folder
	}
	dbPath, root, ok := findProjectDBFrom(cwd)
	if !ok {
		return "", ""
	}
	dsn = "sqlite://" + dbPath
	if dsn == strings.TrimSpace(os.Getenv("MNEMOS_DB_URL")) {
		return "", "" // the repo brain IS the pinned global brain; no overlay
	}
	return dsn, root
}

// withBrainDSN runs fn with MNEMOS_DB_URL temporarily set to dsn, restoring the
// previous value afterward. Hooks are one-shot, single-goroutine processes, so
// repointing the resolver at a second brain for one call is safe.
func withBrainDSN(dsn string, fn func()) {
	prev, had := os.LookupEnv("MNEMOS_DB_URL")
	_ = os.Setenv("MNEMOS_DB_URL", dsn)
	defer func() {
		if had {
			_ = os.Setenv("MNEMOS_DB_URL", prev)
		} else {
			_ = os.Unsetenv("MNEMOS_DB_URL")
		}
	}()
	fn()
}

// hookRecall (UserPromptSubmit) surfaces claims relevant to the user's prompt,
// federating the global brain with the current repo's brain (if the session is
// inside an opted-in repo). Repo claims are more specific, so they lead and win
// on duplicate text.
func hookRecall(ev hookEvent) {
	q := strings.TrimSpace(ev.Prompt)
	if q == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	// Hosted brains are global-only for now (no local repo overlay).
	if hostedConfigured() {
		resp, err := hostedClient(12*time.Second).Search(ctx, q, client.SearchOptions{TopK: 6})
		if err != nil || resp == nil {
			return // fail-open
		}
		claims := make([]recallClaim, 0, len(resp.Claims))
		for _, c := range resp.Claims {
			claims = append(claims, recallClaim{Type: c.Type, Text: c.Text, TrustScore: c.TrustScore, Source: "global"})
		}
		emitContext("UserPromptSubmit", renderRecall(claims, len(resp.Contradictions)))
		return
	}

	// Global brain (the pinned MNEMOS_DB_URL).
	globalClaims, globalContra := recallLocal(ctx, q, "global")

	// Repo overlay, if the session is inside an opted-in repo.
	var repoClaims []recallClaim
	var repoContra int
	if dsn, _ := repoBrain(ev.Cwd); dsn != "" {
		withBrainDSN(dsn, func() {
			repoClaims, repoContra = recallLocal(ctx, q, "workspace")
		})
	}

	claims, contra := mergeRecall(repoClaims, repoContra, globalClaims, globalContra)
	emitContext("UserPromptSubmit", renderRecall(claims, contra))
}

// recallLocal queries the currently-resolved local brain, tagging each claim
// with its source tier. Fail-open: any error yields no claims.
func recallLocal(ctx context.Context, q, source string) ([]recallClaim, int) {
	out, err := mcpRunQuery(ctx, mcpQueryInput{Question: q})
	if err != nil {
		return nil, 0
	}
	claims := make([]recallClaim, 0, len(out.Claims))
	for _, c := range out.Claims {
		claims = append(claims, recallClaim{Type: string(c.Type), Text: c.Text, TrustScore: c.TrustScore, Source: source})
	}
	return claims, len(out.Contradictions)
}

// mergeRecall combines repo (more specific — listed first, wins on duplicate
// text) with global claims, de-duplicating by normalized text.
func mergeRecall(repo []recallClaim, repoContra int, global []recallClaim, globalContra int) ([]recallClaim, int) {
	seen := make(map[string]bool)
	out := make([]recallClaim, 0, len(repo)+len(global))
	for _, tier := range [][]recallClaim{repo, global} {
		for _, c := range tier {
			key := strings.ToLower(strings.TrimSpace(c.Text))
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, c)
		}
	}
	return out, repoContra + globalContra
}

// renderRecall renders a compact, citation-friendly context block. It caps the
// claim list so the injection stays small. When a repo overlay contributed, each
// claim is tagged with its tier so the model knows what's repo-specific.
func renderRecall(claims []recallClaim, contradictions int) string {
	if len(claims) == 0 {
		return ""
	}
	hasRepo := false
	for _, c := range claims {
		if c.Source == "workspace" {
			hasRepo = true
			break
		}
	}
	var b strings.Builder
	b.WriteString("Relevant knowledge from Mnemos (your long-term memory):\n")
	const maxClaims = 6
	for i, c := range claims {
		if i >= maxClaims {
			fmt.Fprintf(&b, "  …and %d more (use the query_knowledge tool for the full set)\n", len(claims)-maxClaims)
			break
		}
		tier := ""
		if hasRepo && c.Source != "" {
			tier = "{" + c.Source + "} "
		}
		fmt.Fprintf(&b, "  - %s[%s] %s (trust %.2f)\n", tier, c.Type, strings.TrimSpace(c.Text), c.TrustScore)
	}
	if contradictions > 0 {
		fmt.Fprintf(&b, "  ⚠ %d contradiction(s) recorded on this topic — verify before relying.\n", contradictions)
	}
	if hasRepo {
		b.WriteString("{workspace} claims are specific to this workspace/repo and override {global} ones on conflict.\n")
	}
	b.WriteString("If this contradicts newer information, prefer the newer and note the conflict.")
	return b.String()
}

// hookBrief (SessionStart) injects a short brain summary at session start.
func hookBrief(ev hookEvent) {
	// Skip on /clear and compaction resumes to avoid re-injecting every time.
	if ev.Source == "clear" || ev.Source == "compact" {
		return
	}
	if hostedConfigured() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		m, err := hostedClient(10 * time.Second).Metrics(ctx)
		if err != nil || m == nil || m.Claims == 0 {
			return
		}
		emitContext("SessionStart", renderBrief(m.Claims, m.Runs, m.Contradictions, 0))
		return
	}

	gm, err := mcpRunMetrics(context.Background())
	if err != nil {
		return
	}
	// Repo overlay, if the session is inside an opted-in repo.
	var repoClaims int64
	if dsn, repoRoot := repoBrain(ev.Cwd); dsn != "" {
		// Sync-back: fold any human edits of the repo's AGENTS.md managed block
		// back into the brain so the loop closes. This is the latency-sensitive
		// SessionStart path, so it runs RULE-BASED (no LLM call) under a short
		// timeout — session start must not hang on a slow/unreachable model. The
		// thorough LLM pass happens at session end (capture) and on `sync-docs`.
		if repoRoot != "" {
			sbCtx, sbCancel := context.WithTimeout(context.Background(), 8*time.Second)
			syncBackFromDocs(sbCtx, repoRoot, "AGENTS.md", dsn, false /* useLLM */)
			sbCancel()
		}
		withBrainDSN(dsn, func() {
			if rm, e := mcpRunMetrics(context.Background()); e == nil {
				repoClaims = rm.Claims
			}
		})
	}
	if gm.Claims == 0 && repoClaims == 0 {
		return // both tiers empty: nothing worth announcing
	}
	emitContext("SessionStart", renderBrief(gm.Claims, gm.Runs, gm.Contradictions, repoClaims))
}

// renderBrief formats the session-start brain summary. repoClaims > 0 adds a
// note that this repo has its own scoped overlay.
func renderBrief(claims, runs, contradictions, repoClaims int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mnemos brain connected: %d claims across %d runs", claims, runs)
	if contradictions > 0 {
		fmt.Fprintf(&b, ", %d open contradiction(s)", contradictions)
	}
	if repoClaims > 0 {
		fmt.Fprintf(&b, "; +%d claim(s) scoped to this workspace", repoClaims)
	}
	b.WriteString(".\n")
	b.WriteString("Use query_knowledge before answering questions about past decisions, and record new decisions/facts with process_text or record_decision.")
	return b.String()
}

// hookCapture (SessionEnd) distills the session transcript into the brain.
// SessionEnd ignores hook stdout, so this is pure side effect. It reads the
// JSONL transcript, extracts the conversational text, caps the size, and runs
// it through the standard ingest→extract→relate pipeline tagged with the
// session id so the run is traceable and de-duplicated on replay.
func hookCapture(ev hookEvent) {
	if strings.TrimSpace(ev.TranscriptPath) == "" {
		return
	}
	text := extractTranscriptText(ev.TranscriptPath, 20*1024)
	if strings.TrimSpace(text) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if hostedConfigured() {
		// Let the server decide LLM/embeddings from ITS config (the hook machine
		// can't know the hosted brain's providers): omit the flags (nil).
		_, _ = hostedClient(90*time.Second).Process(ctx, client.ProcessRequest{Text: text})
		return
	}

	// Local brain: auto-enable LLM/embeddings when a provider is configured on
	// this machine; the pipeline degrades to rule-based extraction otherwise.
	useLLM := strings.TrimSpace(os.Getenv("MNEMOS_LLM_PROVIDER")) != ""
	write := func() {
		_, _ = mcpRunProcessText(ctx, "claude-code", mcpProcessTextInput{
			Text:          text,
			UseLLM:        useLLM,
			UseEmbeddings: useLLM,
		})
	}
	// Repo-first routing: a session inside an opted-in repo captures to the repo
	// brain (keeping repo-specific knowledge local); otherwise the global brain.
	if dsn, repoRoot := repoBrain(ev.Cwd); dsn != "" {
		withBrainDSN(dsn, write)
		// Absorb any human edits of the managed block into the brain (thorough,
		// LLM when configured). We deliberately do NOT regenerate AGENTS.md here:
		// regenerating from the brain would wipe a human note that didn't extract
		// into a claim. Refreshing the committed doc is the explicit, non-
		// destructive `mnemos sync-docs` (which absorbs then regenerates). Best-
		// effort: never fail the hook over docs.
		if repoRoot != "" {
			syncBackFromDocs(ctx, repoRoot, "AGENTS.md", dsn, useLLM)
		}
		return
	}
	write()
}

// transcriptLine is the lenient shape we read from each JSONL transcript row.
// Claude Code's transcript schema varies by version, so we decode defensively:
// role/type plus a content field that may be a string or an array of blocks.
type transcriptLine struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Message json.RawMessage `json:"message"`
	Content json.RawMessage `json:"content"`
}

// extractTranscriptText pulls user prompts and assistant text out of a JSONL
// transcript, newest content first, stopping once maxBytes is reached.
func extractTranscriptText(path string, maxBytes int) string {
	f, err := os.Open(path) //nolint:gosec // path supplied by Claude Code hook payload
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, 4<<20))
	if err != nil {
		return ""
	}
	var b strings.Builder
	for raw := range strings.SplitSeq(string(data), "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var line transcriptLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			continue
		}
		role := line.Role
		if role == "" {
			role = line.Type
		}
		if role != "user" && role != "assistant" {
			continue
		}
		body := line.Message
		if len(body) == 0 {
			body = line.Content
		}
		if txt := extractMessageText(body, line.Content); txt != "" {
			b.WriteString(txt)
			b.WriteByte('\n')
			if b.Len() >= maxBytes {
				break
			}
		}
	}
	return b.String()
}

// extractMessageText coaxes plain text out of a message payload that may be a
// string, an object with a content field (itself a string or block array), or
// a bare array of {type,text} blocks.
func extractMessageText(message, content json.RawMessage) string {
	for _, raw := range []json.RawMessage{message, content} {
		if t := textFromAny(raw); t != "" {
			return t
		}
	}
	return ""
}

// textFromAny recursively resolves text from a raw JSON value that may be a
// string, a block array, or an object with text/content fields.
func textFromAny(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	if t := extractBlocks(raw); t != "" {
		return t
	}
	var obj struct {
		Content json.RawMessage `json:"content"`
		Text    string          `json:"text"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		if strings.TrimSpace(obj.Text) != "" {
			return obj.Text
		}
		if t := textFromAny(obj.Content); t != "" {
			return t
		}
	}
	return ""
}

// extractBlocks joins the text of an Anthropic-style content-block array,
// skipping non-text blocks (tool_use, tool_result, images).
func extractBlocks(raw json.RawMessage) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, blk := range blocks {
		if blk.Type == "text" && strings.TrimSpace(blk.Text) != "" {
			parts = append(parts, blk.Text)
		}
	}
	return strings.Join(parts, "\n")
}
