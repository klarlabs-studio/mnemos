package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/config"
	"go.klarlabs.de/mnemos/internal/store"
)

// `mnemos init` — the system-detecting, one-command setup that wires Mnemos
// into Claude Code as a central brain: it detects the host, creates the brain,
// writes a starter config, registers the MCP server, and installs the recall /
// brief / capture hooks. It previews everything and asks before writing (skip
// with --yes; preview only with --dry-run).
//
// The same plan/apply engine backs the `configure_environment` MCP tool, so
// Claude can finish setup from inside a session.

type initOptions struct {
	project bool            // scope to ./.mnemos instead of the global brain
	dsn     string          // explicit DB DSN override
	url     string          // hosted MCP endpoint (HTTP transport) instead of a local brain
	token   string          // bearer token for the hosted endpoint
	hooks   map[string]bool // which hooks to install: recall|brief|capture
	noHooks bool            // install no hooks
	noMCP   bool            // skip MCP registration (already registered)
	service bool            // scaffold a hosted `mnemos serve` deployment instead
	out     string          // output directory for --service scaffolding (default ".")
	force   bool
	dryRun  bool
	yes     bool
}

func defaultHookSet() map[string]bool {
	return map[string]bool{"recall": true, "brief": true, "capture": true}
}

// parseInitArgs parses init's own flags. The brain DSN arrives via globalDSN
// (the global --db flag, parsed in ParseFlags) rather than a local --db case,
// so `mnemos init --db <dsn>` resolves identically to every other command.
func parseInitArgs(args []string, globalDSN string) (initOptions, error) {
	opts := initOptions{hooks: defaultHookSet(), dsn: globalDSN}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			opts.project = true
		case "--no-hooks":
			opts.noHooks = true
		case "--no-mcp":
			opts.noMCP = true
		case "--url":
			if i+1 >= len(args) {
				return opts, NewUserError("--url requires a hosted MCP endpoint (e.g. https://host/mcp)")
			}
			opts.url = args[i+1]
			i++
		case "--token":
			if i+1 >= len(args) {
				return opts, NewUserError("--token requires a bearer token value")
			}
			opts.token = args[i+1]
			i++
		case "--service":
			// Handled by a dedicated path (handleInit) before plan building.
			opts.service = true
		case "--out":
			if i+1 >= len(args) {
				return opts, NewUserError("--out requires a directory path")
			}
			opts.out = args[i+1]
			i++
		case "--hooks":
			if i+1 >= len(args) {
				return opts, NewUserError("--hooks requires a comma list (recall,brief,capture)")
			}
			opts.hooks = parseHookSelection(args[i+1])
			if len(opts.hooks) == 0 {
				return opts, NewUserError("--hooks: no valid hooks in %q (choose from recall,brief,capture)", args[i+1])
			}
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				return opts, NewUserError("unknown init flag %q", args[i])
			}
			return opts, NewUserError("init takes no positional arguments (got %q)", args[i])
		}
	}
	if opts.noHooks {
		opts.hooks = nil
	}
	if opts.url != "" && opts.dsn != "" {
		return opts, NewUserError("--url (hosted service) and --db (local/hosted database) are mutually exclusive")
	}
	if opts.service && (opts.url != "" || opts.dsn != "") {
		return opts, NewUserError("--service scaffolds a deployment and takes neither --url nor --db")
	}
	return opts, nil
}

func parseHookSelection(csv string) map[string]bool {
	valid := map[string]bool{"recall": true, "brief": true, "capture": true}
	out := map[string]bool{}
	for part := range strings.SplitSeq(csv, ",") {
		p := strings.TrimSpace(part)
		if valid[p] {
			out[p] = true
		}
	}
	return out
}

// handleInit is the CLI entry point for `mnemos init` (and its `setup` alias
// when hooks are requested).
func handleInit(args []string, f Flags) {
	opts, err := parseInitArgs(args, f.DB)
	if err != nil {
		exitWithMnemosError(false, err)
		return
	}
	opts.force = f.Force
	opts.dryRun = f.DryRun
	opts.yes = f.Yes

	// --service is a different deliverable (a deployment bundle, not a client
	// setup), so it has its own path.
	if opts.service {
		runInitService(opts)
		return
	}

	plan := buildInitPlan(opts)
	renderInitPlan(plan)

	if plan.opts.dryRun {
		fmt.Println("\n(dry run — nothing written; re-run without --dry-run to apply)")
		return
	}
	if !plan.opts.yes && !confirm("\nProceed with the changes above?") {
		fmt.Println("aborted — nothing written.")
		return
	}

	res := applyInitPlan(plan)
	renderInitResults(res)

	// Verify only makes sense for a local brain; the hosted endpoint was
	// already probed above.
	if !plan.httpMode {
		fmt.Println("\nVerifying...")
		report := runDoctorChecks(context.Background())
		printDoctorHuman(report)
	}

	printInitNextSteps(plan)
}

// initPlan is the fully-resolved set of changes init will make.
type initPlan struct {
	opts         initOptions
	env          environment
	scope        string
	httpMode     bool   // hosted-service mode: register a remote HTTP MCP endpoint
	url          string // hosted MCP endpoint (httpMode)
	token        string // bearer token (httpMode)
	dsn          string
	backend      string // "sqlite" | "postgres" | "mysql" | "libsql" | "memory" | "other"
	inlineDSN    bool   // safe to embed the DSN in Claude's config (no secret)
	bin          string
	brainDir     string            // sqlite parent dir to create; "" for networked backends
	configPath   string            // config file to write (when configKV non-empty)
	configKV     map[string]string // dotted keys to merge into the config file
	registerMCP  bool
	settingsPath string
	specs        []hookSpec
}

func buildInitPlan(opts initOptions) initPlan {
	env := detectEnvironment()
	scope := "user"
	if opts.project {
		scope = "project"
	}

	// Hosted-service mode: point Claude Code at a remote MCP endpoint over HTTP.
	// There is no local brain, but we DO write a 0600 config (the hosted URL +
	// token) and install the recall/brief/capture hooks — the hooks discover the
	// endpoint from that config and call the REST API instead of a local store.
	if opts.url != "" {
		plan := initPlan{
			opts:        opts,
			env:         env,
			scope:       scope,
			httpMode:    true,
			url:         opts.url,
			token:       opts.token,
			bin:         selfPath(),
			registerMCP: !opts.noMCP,
			inlineDSN:   false, // never inline a secret; hooks read it from config
			specs:       hookSpecsFor(opts.hooks),
		}
		// The hosted URL (and token, if any) live in the 0600 config file, never
		// inlined into Claude Code's settings. The token is a bearer credential.
		configKV := map[string]string{"server.url": opts.url}
		if opts.token != "" {
			configKV["server.token"] = opts.token
		}
		plan.configPath = initConfigPath(opts.project)
		plan.configKV = configKV
		if len(plan.specs) > 0 {
			plan.settingsPath = claudeSettingsPath(opts.project)
		}
		return plan
	}

	dsn := opts.dsn
	if dsn == "" {
		if opts.project {
			dsn = "sqlite://" + projectDBPath()
		} else {
			dsn = "sqlite://" + globalDBPath()
		}
	}
	inline := dsnInlineSafe(dsn)
	brainDir := ""
	if p, ok := sqliteFilePath(dsn); ok {
		brainDir = filepath.Dir(p)
	}

	plan := initPlan{
		opts:        opts,
		env:         env,
		scope:       scope,
		dsn:         dsn,
		backend:     dsnBackend(dsn),
		inlineDSN:   inline,
		bin:         selfPath(),
		brainDir:    brainDir,
		registerMCP: !opts.noMCP,
		specs:       hookSpecsFor(opts.hooks),
	}

	// Compose the config file the server/hooks discover at runtime.
	configKV := map[string]string{}
	// A networked DSN carries credentials — persist it to the 0600 config file
	// instead of inlining it into Claude Code's settings.
	if !inline {
		configKV["db.url"] = dsn
	}
	// Add a starter LLM block for a key-free local Ollama, but only when no
	// config exists yet (or --force) so we never override operator LLM config.
	plan.configPath = initConfigPath(opts.project)
	if env.LLM.Source == "ollama" && (opts.force || !fileExists(plan.configPath)) {
		configKV["llm.provider"] = env.LLM.Provider
		configKV["llm.model"] = env.LLM.Model
		configKV["llm.base_url"] = env.LLM.BaseURL
		configKV["embed.provider"] = env.LLM.Provider
		configKV["embed.model"] = "nomic-embed-text"
		configKV["embed.base_url"] = env.LLM.BaseURL
	}
	if len(configKV) > 0 {
		plan.configKV = configKV
	} else {
		plan.configPath = ""
	}

	if len(plan.specs) > 0 {
		plan.settingsPath = claudeSettingsPath(opts.project)
	}
	return plan
}

func renderInitPlan(p initPlan) {
	e := p.env
	fmt.Println("mnemos init — set up a central brain for Claude Code")
	fmt.Println()
	fmt.Println("Detected:")
	fmt.Printf("  system:      %s/%s\n", e.OS, e.Arch)
	fmt.Printf("  claude code: %s\n", presence(e.ClaudeCLI || e.ClaudeDir, claudeDetail(e)))
	if e.Cursor {
		fmt.Println("  cursor:      present (not configured by init; see docs/integrations.md)")
	}
	if e.Desktop {
		fmt.Println("  desktop:     Claude Desktop present (not configured by init; see docs/integrations.md)")
	}
	fmt.Printf("  llm:         %s\n", llmDetail(e.LLM))
	if e.LLM.Advisory != "" {
		fmt.Printf("               ↳ %s\n", e.LLM.Advisory)
	}
	fmt.Println()

	if p.httpMode {
		fmt.Println("Will apply (hosted service):")
		fmt.Printf("  • endpoint:  %s  (HTTP MCP transport)\n", p.url)
		if p.token != "" {
			fmt.Println("  • auth:      Authorization: Bearer <token> header")
		} else {
			fmt.Println("  • auth:      none (endpoint is unauthenticated or you'll add a header later)")
		}
		if len(p.configKV) > 0 {
			secret := ""
			if p.token != "" {
				secret = " — URL + token kept here (0600), not in Claude config"
			}
			fmt.Printf("  • config:    %s%s\n", p.configPath, secret)
		}
		fmt.Printf("  • mcp:       register remote `mnemos` server (scope: %s)%s\n", p.scope, mcpFallbackNote(p.env))
		if len(p.specs) > 0 {
			fmt.Printf("  • hooks:     %s → %s  (call the hosted brain over REST)\n", strings.Join(hookNames(p.specs), ", "), p.settingsPath)
		} else {
			fmt.Println("  • hooks:     none (MCP tools only)")
		}
		return
	}

	fmt.Println("Will apply:")
	fmt.Printf("  • brain:     %s  (%s, scope: %s)\n", p.dsn, p.backend, p.scope)
	if len(p.configKV) > 0 {
		secret := ""
		if !p.inlineDSN {
			secret = " — DSN kept here (0600), not in Claude config"
		}
		fmt.Printf("  • config:    %s%s\n", p.configPath, secret)
	}
	if p.registerMCP {
		fmt.Printf("  • mcp:       register `mnemos mcp` (scope: %s)%s\n", p.scope, mcpFallbackNote(p.env))
	} else {
		fmt.Println("  • mcp:       skip (already registered)")
	}
	if len(p.specs) > 0 {
		fmt.Printf("  • hooks:     %s → %s\n", strings.Join(hookNames(p.specs), ", "), p.settingsPath)
	} else {
		fmt.Println("  • hooks:     none (MCP tools only)")
	}
}

// initResult records what actually happened, step by step.
type initResult struct {
	lines []string
	fail  bool
}

func (r *initResult) ok(format string, a ...any) {
	r.lines = append(r.lines, "✓ "+fmt.Sprintf(format, a...))
}
func (r *initResult) skip(format string, a ...any) {
	r.lines = append(r.lines, "· "+fmt.Sprintf(format, a...))
}
func (r *initResult) err(format string, a ...any) {
	r.lines = append(r.lines, "✗ "+fmt.Sprintf(format, a...))
	r.fail = true
}

// applyInitPlan performs the plan and returns a step-by-step result. It never
// exits the process, so both the CLI and the MCP tool can use it and render
// however they like. Individual step failures are collected, not fatal.
func applyInitPlan(p initPlan) initResult {
	var r initResult

	// Mode-specific setup: hosted probes the endpoint + registers the remote MCP;
	// local creates the brain dir, bootstraps the schema, and registers the local
	// MCP. Both then share the config + hooks steps below.
	if p.httpMode {
		if err := probeURL(p.url); err != nil {
			if !p.opts.force {
				r.err("cannot reach %s: %s", p.url, err)
				return r
			}
			r.skip("endpoint unreachable (%s) — continuing anyway (--force)", err)
		} else {
			r.ok("endpoint reachable: %s", p.url)
		}
		if p.registerMCP {
			if err := registerClaudeCodeHTTP(p.url, p.token, p.scope, p.opts.force, false); err != nil {
				r.err("register remote MCP: %s", err)
			} else {
				r.ok("remote MCP server registered (scope: %s)", p.scope)
			}
		}
	} else {
		// 1. Brain directory (sqlite only; networked backends need no local dir).
		if p.brainDir != "" {
			if err := os.MkdirAll(p.brainDir, 0o750); err != nil {
				r.err("create brain dir %s: %s", p.brainDir, err)
				return r
			}
		}

		// 2. Connectivity check — actually open the chosen DSN (this also runs the
		// idempotent schema bootstrap). A dead DSN is fatal unless --force, so we
		// never wire hooks/MCP at a brain that can't be reached.
		if err := probeBrain(p.dsn); err != nil {
			if !p.opts.force {
				r.err("cannot reach brain %s: %s", p.dsn, err)
				return r
			}
			r.skip("brain unreachable (%s) — continuing anyway (--force)", err)
		} else {
			r.ok("brain reachable, schema ready (%s)", p.backend)
		}
		// Point this process's doctor probe (and any child) at the chosen DSN.
		_ = os.Setenv("MNEMOS_DB_URL", p.dsn)

		// 4. MCP registration (local stdio server).
		if p.registerMCP {
			if err := registerClaudeCode(p.bin, p.dsn, p.scope, p.inlineDSN, p.opts.force, false); err != nil {
				r.err("register MCP: %s", err)
			} else {
				r.ok("MCP server registered (scope: %s)", p.scope)
			}
		}
	}

	// 3. Config file (merged, never clobbered). Holds the credentialed DSN
	// (local networked backend) or the hosted URL + token (hosted mode).
	if len(p.configKV) > 0 {
		err := config.SetValues(p.configPath, p.configKV)
		switch {
		case err != nil:
			r.err("write config %s: %s", p.configPath, err)
		case p.httpMode:
			r.ok("wrote config %s (hosted URL/token stored here, 0600 — not in Claude config)", p.configPath)
		case p.inlineDSN:
			r.ok("wrote config %s", p.configPath)
		default:
			r.ok("wrote config %s (DSN stored here, 0600 — not in Claude config)", p.configPath)
		}
	}

	// 5. Hooks. For hosted mode p.dsn is empty and inlineDSN false, so the hook
	// command carries no --db and discovers the hosted URL/token from the config
	// at runtime, routing recall/brief/capture through the REST API.
	if len(p.specs) > 0 {
		backup, err := installHooks(p.settingsPath, p.bin, p.dsn, p.specs, p.inlineDSN)
		switch {
		case err != nil:
			r.err("install hooks in %s: %s", p.settingsPath, err)
		case backup != "":
			r.ok("installed %d hook(s) in %s (backup: %s)", len(p.specs), p.settingsPath, backup)
		default:
			r.ok("installed %d hook(s) in %s", len(p.specs), p.settingsPath)
		}
	}
	return r
}

// probeBrain opens the DSN through the store registry with a short timeout,
// proving the backend is reachable and bootstrapping its schema. The open is
// closed immediately; init only needs to know it works.
func probeBrain(dsn string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		return err
	}
	return conn.Close()
}

// probeURL checks a hosted MCP endpoint is reachable. Any HTTP response
// (including 401/403/405) counts as reachable — we only care that the host
// answers; auth/method specifics are the transport's concern at runtime.
func probeURL(rawURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func renderInitResults(r initResult) {
	fmt.Println("\nApplied:")
	for _, l := range r.lines {
		fmt.Printf("  %s\n", l)
	}
}

func printInitNextSteps(p initPlan) {
	if p.httpMode {
		fmt.Println("\nNext steps:")
		fmt.Println("  1. Restart Claude Code so it connects to the remote MCP server.")
		fmt.Println("  2. Run  /mcp  (or  claude mcp list )  to confirm mnemos shows Connected.")
		if len(p.specs) > 0 {
			fmt.Println("  3. Run  /hooks  to see the recall/brief/capture hooks. They call the hosted")
			fmt.Println("     brain over REST and fire automatically:")
			for _, s := range p.specs {
				fmt.Printf("       - %s\n", hookDescription(s.Sub))
			}
		} else {
			fmt.Println("  3. Ask Claude to \"remember\" things or \"what do we know about …\" — the hosted")
			fmt.Println("     brain answers over HTTP via the MCP tools.")
		}
		return
	}
	fmt.Println("\nNext steps:")
	fmt.Println("  1. Restart Claude Code (stdio MCP servers reconnect on a fresh session).")
	if p.scope == "project" {
		fmt.Println("     Project-scoped MCP servers prompt for workspace trust on first launch.")
	}
	fmt.Println("  2. Run  /mcp  (or  claude mcp list )  to confirm mnemos is connected.")
	if len(p.specs) > 0 {
		fmt.Println("  3. Run  /hooks  to see the recall/brief/capture hooks. They fire automatically:")
		for _, s := range p.specs {
			fmt.Printf("       - %s\n", hookDescription(s.Sub))
		}
	} else {
		fmt.Println("  3. Ask Claude to \"remember\" things or \"what do we know about …\" to use the MCP tools.")
	}
	if p.env.LLM.Source == "none" {
		fmt.Println("\nRunning zero-config (rule-based). For LLM extraction + semantic search, install Ollama")
		fmt.Println("or set MNEMOS_LLM_PROVIDER / MNEMOS_LLM_API_KEY (see mnemos.example.yaml).")
	}
}

// runInitService scaffolds a hosted `mnemos serve` deployment (compose + config
// + env + README) into the output directory. It previews the file list and
// confirms before writing (unless --yes / --dry-run).
func runInitService(opts initOptions) {
	outDir := opts.out
	if outDir == "" {
		outDir = "."
	}
	fmt.Println("mnemos init --service — scaffold a hosted mnemos serve deployment")
	fmt.Printf("\nWill write into: %s\n", outDir)
	fmt.Println("  • docker-compose.yml       (mnemos serve + postgres)")
	fmt.Println("  • mnemos.yaml              (config; secrets come from the environment)")
	fmt.Println("  • .env.example            (POSTGRES_PASSWORD, MNEMOS_JWT_SECRET)")
	fmt.Println("  • README-mnemos-service.md (run + client instructions)")
	if opts.force {
		fmt.Println("  (--force: existing files will be overwritten)")
	}

	if opts.dryRun {
		fmt.Println("\n(dry run — nothing written)")
		return
	}
	if !opts.yes && !confirm("\nWrite these files?") {
		fmt.Println("aborted — nothing written.")
		return
	}

	written, err := scaffoldService(outDir, opts.force)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "scaffold service deployment"))
		return
	}
	fmt.Println("\nApplied:")
	for _, l := range written {
		fmt.Printf("  %s\n", l)
	}
	fmt.Println("\nNext steps:")
	fmt.Printf("  1. cd %s && cp .env.example .env   # then fill in POSTGRES_PASSWORD + MNEMOS_JWT_SECRET\n", outDir)
	fmt.Println("  2. docker compose up -d            # starts mnemos serve + postgres")
	fmt.Println("  3. curl localhost:8080/health      # confirm it's live")
	fmt.Println("  4. Point clients at it: REST/gRPC via the SDK, or Claude Code via")
	fmt.Println("       mnemos init --url http://<host>:8080/mcp --token <jwt>")
	fmt.Println("     (expose the MCP endpoint with: mnemos mcp --http :8081 --auth)")
}

// ---- rendering helpers ----

func presence(ok bool, detail string) string {
	if ok {
		return "detected — " + detail
	}
	return "not detected — install Claude Code, then re-run"
}

func claudeDetail(e environment) string {
	switch {
	case e.ClaudeCLI:
		return "`claude` CLI on PATH"
	case e.ClaudeDir:
		return "~/.claude present (CLI not on PATH)"
	default:
		return ""
	}
}

func llmDetail(d llmDetect) string {
	switch d.Source {
	case "ollama":
		return fmt.Sprintf("Ollama (local, key-free) — provider=%s model=%s", d.Provider, d.Model)
	case "env":
		return fmt.Sprintf("configured via env — provider=%s", d.Provider)
	case "vendor-key":
		return fmt.Sprintf("vendor key present (%s) — not auto-wired", d.Provider)
	default:
		return "none — rule-based extraction (Mnemos still works offline)"
	}
}

func mcpFallbackNote(e environment) string {
	if e.ClaudeCLI {
		return ""
	}
	return "  (claude CLI not found — init will print the command/JSON instead)"
}

func hookNames(specs []hookSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Sub)
	}
	return out
}

func hookDescription(sub string) string {
	switch sub {
	case "recall":
		return "recall: injects relevant claims before Claude answers (UserPromptSubmit)"
	case "brief":
		return "brief: injects a brain summary at session start (SessionStart)"
	case "capture":
		return "capture: records the session into the brain at session end (SessionEnd)"
	default:
		return sub
	}
}

// ---- path + content helpers ----

// claudeSettingsPath returns the settings.json to install hooks into: the
// project .claude/settings.json for --project, else the global user file.
func claudeSettingsPath(project bool) string {
	if project {
		cwd, err := os.Getwd()
		if err != nil {
			return filepath.Join(".claude", "settings.json")
		}
		return filepath.Join(cwd, ".claude", "settings.json")
	}
	return homeJoin(".claude", "settings.json")
}

// initConfigPath returns where a starter config would live: project
// .mnemos/mnemos.yaml for --project, else the XDG user config.
func initConfigPath(project bool) string {
	if project {
		cwd, err := os.Getwd()
		if err != nil {
			return filepath.Join(".mnemos", "mnemos.yaml")
		}
		return filepath.Join(cwd, ".mnemos", "mnemos.yaml")
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = homeJoin(".config")
	}
	return filepath.Join(base, "mnemos", "config.yaml")
}

// dsnBackend names the storage backend behind a DSN for display.
func dsnBackend(dsn string) string {
	switch {
	case strings.HasPrefix(dsn, "sqlite://"), strings.HasPrefix(dsn, "sqlite3://"), strings.HasPrefix(dsn, "file://"):
		return "sqlite"
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		return "postgres"
	case strings.HasPrefix(dsn, "mysql://"), strings.HasPrefix(dsn, "mariadb://"):
		return "mysql"
	case strings.HasPrefix(dsn, "libsql://"):
		return "libsql"
	case strings.HasPrefix(dsn, "memory://"):
		return "memory"
	default:
		return "other"
	}
}

// ---- MCP-triggered configuration (configure_environment tool) ----

type mcpConfigureInput struct {
	Hooks   string `json:"hooks,omitempty" jsonschema:"description=Comma list of hooks to install: recall,brief,capture (default: all three)"`
	Project bool   `json:"project,omitempty" jsonschema:"description=Install into the project .claude/settings.json instead of the user-global file"`
	Force   bool   `json:"force,omitempty" jsonschema:"description=Overwrite an existing starter config and replace prior hook entries"`
}

type mcpConfigureOutput struct {
	OK          bool     `json:"ok"`
	Applied     []string `json:"applied"`
	DetectedOS  string   `json:"detectedOs"`
	DetectedLLM string   `json:"detectedLlm"`
	NextSteps   string   `json:"nextSteps"`
}

// mcpRunConfigure runs the init plan/apply engine from inside the MCP server.
// The server is already registered, so it skips MCP registration and only
// writes the starter config and installs the hooks — pinned to the exact brain
// DSN this server is serving, so hooks and server always agree.
func mcpRunConfigure(input mcpConfigureInput) (mcpConfigureOutput, error) {
	opts := initOptions{
		project: input.Project,
		force:   input.Force,
		noMCP:   true,
		yes:     true,
		hooks:   defaultHookSet(),
		dsn:     resolveDSN(),
	}
	if strings.TrimSpace(input.Hooks) != "" {
		opts.hooks = parseHookSelection(input.Hooks)
	}
	plan := buildInitPlan(opts)
	res := applyInitPlan(plan)
	return mcpConfigureOutput{
		OK:          !res.fail,
		Applied:     res.lines,
		DetectedOS:  plan.env.OS + "/" + plan.env.Arch,
		DetectedLLM: llmDetail(plan.env.LLM),
		NextSteps:   "Restart Claude Code (or it may pick hooks up live), then run /hooks and /mcp to confirm. Recall/brief/capture now fire automatically.",
	}, nil
}
