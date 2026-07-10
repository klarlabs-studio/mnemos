package main

import (
	"strings"
	"testing"
)

func TestParseInitArgs(t *testing.T) {
	t.Run("defaults to all hooks, user scope", func(t *testing.T) {
		opts, err := parseInitArgs(nil)
		if err != nil {
			t.Fatal(err)
		}
		if opts.project || opts.noMCP {
			t.Errorf("unexpected defaults: %+v", opts)
		}
		for _, h := range []string{"recall", "brief", "capture"} {
			if !opts.hooks[h] {
				t.Errorf("hook %s should be on by default", h)
			}
		}
	})

	t.Run("no-hooks clears the set", func(t *testing.T) {
		opts, err := parseInitArgs([]string{"--no-hooks"})
		if err != nil {
			t.Fatal(err)
		}
		if len(opts.hooks) != 0 {
			t.Errorf("--no-hooks should empty the set, got %+v", opts.hooks)
		}
	})

	t.Run("hooks subset", func(t *testing.T) {
		opts, err := parseInitArgs([]string{"--hooks", "recall,capture"})
		if err != nil {
			t.Fatal(err)
		}
		if !opts.hooks["recall"] || !opts.hooks["capture"] || opts.hooks["brief"] {
			t.Errorf("subset wrong: %+v", opts.hooks)
		}
	})

	t.Run("invalid hooks errors", func(t *testing.T) {
		if _, err := parseInitArgs([]string{"--hooks", "nope"}); err == nil {
			t.Error("expected error for invalid hooks selection")
		}
	})

	t.Run("project and db", func(t *testing.T) {
		opts, err := parseInitArgs([]string{"--project", "--db", "postgres://x/y"})
		if err != nil {
			t.Fatal(err)
		}
		if !opts.project || opts.dsn != "postgres://x/y" {
			t.Errorf("got %+v", opts)
		}
	})

	t.Run("positional arg rejected", func(t *testing.T) {
		if _, err := parseInitArgs([]string{"claude-code"}); err == nil {
			t.Error("init should reject positional args")
		}
	})

	t.Run("unknown flag rejected", func(t *testing.T) {
		if _, err := parseInitArgs([]string{"--wat"}); err == nil {
			t.Error("expected error for unknown flag")
		}
	})

	t.Run("url and token", func(t *testing.T) {
		opts, err := parseInitArgs([]string{"--url", "https://h/mcp", "--token", "jwt"})
		if err != nil {
			t.Fatal(err)
		}
		if opts.url != "https://h/mcp" || opts.token != "jwt" {
			t.Errorf("got %+v", opts)
		}
	})

	t.Run("url and db mutually exclusive", func(t *testing.T) {
		if _, err := parseInitArgs([]string{"--url", "https://h/mcp", "--db", "sqlite://x"}); err == nil {
			t.Error("expected mutual-exclusion error")
		}
	})

	t.Run("service takes no url/db", func(t *testing.T) {
		if _, err := parseInitArgs([]string{"--service", "--db", "sqlite://x"}); err == nil {
			t.Error("expected error: --service with --db")
		}
		opts, err := parseInitArgs([]string{"--service", "--out", "deploy"})
		if err != nil {
			t.Fatal(err)
		}
		if !opts.service || opts.out != "deploy" {
			t.Errorf("got %+v", opts)
		}
	})
}

func TestBuildInitPlanURLMode(t *testing.T) {
	plan := buildInitPlan(initOptions{url: "https://pet-medical/mcp", token: "jwt", hooks: defaultHookSet()})
	if !plan.httpMode {
		t.Fatal("expected httpMode for --url")
	}
	if plan.url != "https://pet-medical/mcp" || plan.token != "jwt" {
		t.Errorf("url/token not carried: %+v", plan)
	}
	// Hosted mode now installs the recall/brief/capture hooks (they call the
	// hosted brain over REST) and writes the URL/token to the 0600 config.
	if len(plan.specs) == 0 {
		t.Error("hosted mode should install hooks")
	}
	if plan.dsn != "" {
		t.Errorf("hosted mode has no local brain DSN, got %q", plan.dsn)
	}
	if plan.inlineDSN {
		t.Error("hosted mode must never inline a secret into Claude config")
	}
	if plan.configKV["server.url"] != "https://pet-medical/mcp" || plan.configKV["server.token"] != "jwt" {
		t.Errorf("url/token not written to config: %v", plan.configKV)
	}
	if plan.configPath == "" || plan.settingsPath == "" {
		t.Errorf("hosted mode should set configPath (%q) and settingsPath (%q)", plan.configPath, plan.settingsPath)
	}
}

// TestBuildInitPlanURLMode_NoToken confirms a token-free hosted endpoint writes
// only server.url (no empty token key).
func TestBuildInitPlanURLMode_NoToken(t *testing.T) {
	plan := buildInitPlan(initOptions{url: "https://open/mcp", hooks: defaultHookSet()})
	if plan.configKV["server.url"] != "https://open/mcp" {
		t.Errorf("server.url = %q", plan.configKV["server.url"])
	}
	if _, ok := plan.configKV["server.token"]; ok {
		t.Error("no token → server.token must be omitted")
	}
}

func TestDSNBackend(t *testing.T) {
	cases := map[string]string{
		"sqlite:///a.db":         "sqlite",
		"postgres://h/db":        "postgres",
		"postgresql://h/db":      "postgres",
		"mysql://h/db":           "mysql",
		"libsql://h?authToken=x": "libsql",
		"memory://":              "memory",
		"weird://x":              "other",
	}
	for dsn, want := range cases {
		if got := dsnBackend(dsn); got != want {
			t.Errorf("dsnBackend(%q) = %q, want %q", dsn, got, want)
		}
	}
}

func TestDSNInlineSafe(t *testing.T) {
	safe := []string{"sqlite:///a.db", "memory://"}
	unsafe := []string{"postgres://u:p@h/db", "mysql://u:p@h/db", "libsql://h?authToken=x"}
	for _, d := range safe {
		if !dsnInlineSafe(d) {
			t.Errorf("%q should be inline-safe", d)
		}
	}
	for _, d := range unsafe {
		if dsnInlineSafe(d) {
			t.Errorf("%q should NOT be inline-safe (carries a credential)", d)
		}
	}
}

func TestHookSpecsFor(t *testing.T) {
	specs := hookSpecsFor(map[string]bool{"recall": true, "brief": true, "capture": true})
	byEvent := map[string]hookSpec{}
	for _, s := range specs {
		byEvent[s.Event] = s
	}
	if byEvent["UserPromptSubmit"].Sub != "recall" {
		t.Error("recall should bind UserPromptSubmit")
	}
	if byEvent["SessionStart"].Matcher != "startup|resume" {
		t.Errorf("brief matcher = %q, want startup|resume", byEvent["SessionStart"].Matcher)
	}
	if byEvent["SessionEnd"].Sub != "capture" {
		t.Error("capture should bind SessionEnd")
	}
	if len(hookSpecsFor(map[string]bool{})) != 0 {
		t.Error("empty selection should yield no specs")
	}
}

func TestInitConfigPathScope(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got := initConfigPath(false); got != "/xdg/mnemos/config.yaml" {
		t.Errorf("user config path = %q", got)
	}
	if got := initConfigPath(true); !strings.HasSuffix(got, "/.mnemos/mnemos.yaml") {
		t.Errorf("project config path = %q, want .../.mnemos/mnemos.yaml", got)
	}
}

func TestBuildInitPlanUserScopeDefaults(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "") // force default resolution
	t.Setenv("XDG_DATA_HOME", "/data")
	plan := buildInitPlan(initOptions{hooks: defaultHookSet()})
	if plan.scope != "user" {
		t.Errorf("scope = %q, want user", plan.scope)
	}
	if !strings.HasPrefix(plan.dsn, "sqlite:///data/mnemos/") {
		t.Errorf("dsn = %q, want global XDG data path", plan.dsn)
	}
	if !plan.registerMCP {
		t.Error("registerMCP should default true")
	}
	if len(plan.specs) != 3 {
		t.Errorf("expected 3 hook specs, got %d", len(plan.specs))
	}
}
