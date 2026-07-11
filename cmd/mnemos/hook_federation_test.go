package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoBrainDSN(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MNEMOS_URL", "") // not hosted
	// An opted-in repo below home.
	repo := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(repo, ".mnemos"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "pkg", "x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// The global brain is elsewhere.
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(home, ".local/share/mnemos/mnemos.db"))

	want := "sqlite://" + filepath.Join(repo, ".mnemos", "mnemos.db")
	if got, root := repoBrain(sub); got != want || root != repo {
		t.Errorf("repoBrain(%q) = (%q, %q), want (%q, %q)", sub, got, root, want, repo)
	}

	// Outside any repo (a bare dir under home) → no overlay.
	if got, _ := repoBrain(home); got != "" {
		t.Errorf("no repo → want empty, got %q", got)
	}

	// Empty cwd → no overlay.
	if got, _ := repoBrain(""); got != "" {
		t.Errorf("empty cwd → want empty, got %q", got)
	}

	// When the repo brain IS the pinned global brain, it is not an overlay.
	t.Setenv("MNEMOS_DB_URL", want)
	if got, _ := repoBrain(sub); got != "" {
		t.Errorf("repo brain == global → want empty, got %q", got)
	}
}

func TestRepoBrainDSN_HostedIsGlobalOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(repo, ".mnemos"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MNEMOS_URL", "https://brain.example.com") // hosted
	if got, _ := repoBrain(repo); got != "" {
		t.Errorf("hosted mode has no repo overlay, got %q", got)
	}
}

func TestWithBrainDSN_RestoresEnv(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "sqlite:///global.db")
	withBrainDSN("sqlite:///repo.db", func() {
		if os.Getenv("MNEMOS_DB_URL") != "sqlite:///repo.db" {
			t.Error("MNEMOS_DB_URL not repointed inside withBrainDSN")
		}
	})
	if os.Getenv("MNEMOS_DB_URL") != "sqlite:///global.db" {
		t.Error("MNEMOS_DB_URL not restored after withBrainDSN")
	}

	// When unset before, it is unset after.
	_ = os.Unsetenv("MNEMOS_DB_URL")
	withBrainDSN("sqlite:///repo.db", func() {})
	if _, present := os.LookupEnv("MNEMOS_DB_URL"); present {
		t.Error("MNEMOS_DB_URL should be unset after withBrainDSN when it started unset")
	}
}

func TestMergeRecall_RepoWinsAndDedups(t *testing.T) {
	repo := []recallClaim{
		{Text: "uses Kafka", Source: "workspace"},
		{Text: "shared fact", Source: "workspace"},
	}
	global := []recallClaim{
		{Text: "shared fact", Source: "global"}, // duplicate → repo wins
		{Text: "general pref", Source: "global"},
	}
	merged, contra := mergeRecall(repo, 1, global, 2)
	if len(merged) != 3 {
		t.Fatalf("want 3 merged claims, got %d: %+v", len(merged), merged)
	}
	// Repo claims lead.
	if merged[0].Source != "workspace" || merged[1].Source != "workspace" {
		t.Errorf("repo claims should lead: %+v", merged)
	}
	// The shared fact is attributed to repo, not global.
	for _, c := range merged {
		if c.Text == "shared fact" && c.Source != "workspace" {
			t.Errorf("duplicate should resolve to repo, got %q", c.Source)
		}
	}
	if contra != 3 {
		t.Errorf("contradictions should sum: want 3, got %d", contra)
	}
}

func TestRenderRecall_TagsOnlyWhenRepoPresent(t *testing.T) {
	// Global-only: no tier tags, no repo footer.
	globalOnly := renderRecall([]recallClaim{{Type: "fact", Text: "x", Source: "global"}}, 0)
	if strings.Contains(globalOnly, "{global}") || strings.Contains(globalOnly, "scoped to this workspace") {
		t.Errorf("global-only recall should not tag tiers: %q", globalOnly)
	}
	// With a repo overlay: tags appear and the precedence note is shown.
	mixed := renderRecall([]recallClaim{
		{Type: "fact", Text: "r", Source: "workspace"},
		{Type: "fact", Text: "g", Source: "global"},
	}, 0)
	if !strings.Contains(mixed, "{workspace}") || !strings.Contains(mixed, "{global}") {
		t.Errorf("mixed recall should tag both tiers: %q", mixed)
	}
	if !strings.Contains(mixed, "override {global}") {
		t.Errorf("mixed recall should explain precedence: %q", mixed)
	}
}
