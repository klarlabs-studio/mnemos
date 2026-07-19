package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every skill directory in the embedded FS must be registered in skillNames,
// or it ships in the binary and never installs.
func TestSkills_EmbeddedMatchesRegistered(t *testing.T) {
	embedded := embeddedSkillNames()
	registered := slices.Clone(skillNames)
	slices.Sort(registered)
	if !slices.Equal(embedded, registered) {
		t.Fatalf("embedded skills %v != registered skillNames %v", embedded, registered)
	}
}

// Claude Code only loads a SKILL.md with YAML frontmatter carrying name and
// description, and the name must match the directory it lives in.
func TestSkills_FrontmatterIsValid(t *testing.T) {
	for _, name := range skillNames {
		body, err := skillContent(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.HasPrefix(body, "---\n") {
			t.Errorf("%s: SKILL.md must open with YAML frontmatter", name)
			continue
		}
		end := strings.Index(body[4:], "\n---\n")
		if end < 0 {
			t.Errorf("%s: unterminated frontmatter", name)
			continue
		}
		front := body[4 : 4+end]
		if got := frontmatterField(front, "name"); got != name {
			t.Errorf("%s: frontmatter name = %q, want %q (must match the directory)", name, got, name)
		}
		if desc := frontmatterField(front, "description"); len(desc) < 40 {
			t.Errorf("%s: description is %d chars; too thin to trigger reliably", name, len(desc))
		}
	}
}

// Bare "brief"/"capture" would collide with other tools' skills of the same
// name (the Agent OS memory skills use exactly those), silently shadowing one.
func TestSkills_NamesArePrefixed(t *testing.T) {
	for _, name := range skillNames {
		if !strings.HasPrefix(name, "mnemos-") {
			t.Errorf("skill %q must be prefixed mnemos- to avoid colliding with other skills", name)
		}
	}
}

func TestInstallSkills_CreatesFiles(t *testing.T) {
	dir := t.TempDir()
	results, err := installSkills(dir)
	if err != nil {
		t.Fatalf("installSkills: %v", err)
	}
	if len(results) != len(skillNames) {
		t.Fatalf("got %d results, want %d", len(results), len(skillNames))
	}
	for _, res := range results {
		if !res.Changed {
			t.Errorf("%s: Changed=false on a fresh install", res.Name)
		}
		if res.Backup != "" {
			t.Errorf("%s: backed up %q with no prior file", res.Name, res.Backup)
		}
		want, _ := skillContent(res.Name)
		got, readErr := os.ReadFile(filepath.Join(dir, res.Name, "SKILL.md"))
		if readErr != nil {
			t.Fatalf("%s: %v", res.Name, readErr)
		}
		if string(got) != want {
			t.Errorf("%s: written content differs from embedded", res.Name)
		}
	}
}

// Re-running init must not churn files or spam .bak-mnemos copies.
func TestInstallSkills_IdempotentOnSecondRun(t *testing.T) {
	dir := t.TempDir()
	if _, err := installSkills(dir); err != nil {
		t.Fatalf("first install: %v", err)
	}
	results, err := installSkills(dir)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	for _, res := range results {
		if res.Changed {
			t.Errorf("%s: Changed=true on an unmodified re-install", res.Name)
		}
		if res.Backup != "" {
			t.Errorf("%s: made a backup with identical content", res.Name)
		}
		if _, statErr := os.Stat(res.Path + ".bak-mnemos"); statErr == nil {
			t.Errorf("%s: created a backup file on a no-op install", res.Name)
		}
	}
}

// A user edit (or a foreign skill of the same name) must survive as a backup
// rather than being destroyed.
func TestInstallSkills_BacksUpDivergentContent(t *testing.T) {
	dir := t.TempDir()
	name := skillNames[0]
	path := filepath.Join(dir, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	const edited = "---\nname: mine\n---\nhand-edited, do not lose\n"
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := installSkills(dir)
	if err != nil {
		t.Fatalf("installSkills: %v", err)
	}
	var got skillResult
	for _, res := range results {
		if res.Name == name {
			got = res
		}
	}
	if !got.Changed {
		t.Errorf("%s: Changed=false despite differing content", name)
	}
	if got.Backup == "" {
		t.Fatalf("%s: replaced divergent content without a backup", name)
	}
	saved, err := os.ReadFile(got.Backup)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(saved) != edited {
		t.Errorf("backup lost the prior content: got %q", saved)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := skillContent(name)
	if string(current) != want {
		t.Errorf("%s: not replaced with the embedded version", name)
	}
}

// Skills carry no DSN and no token, so unlike hooks they are safe to write
// verbatim in every mode. Guard that a credential never leaks in by edit.
func TestSkills_ContainNoCredentials(t *testing.T) {
	banned := []string{"MNEMOS_LLM_API_KEY=", "server.token", "Bearer ey", "postgres://", "--db "}
	for _, name := range skillNames {
		body, err := skillContent(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range banned {
			if strings.Contains(body, b) {
				t.Errorf("%s: contains %q — skills must stay credential-free", name, b)
			}
		}
	}
}

func TestParseInitArgs_NoSkills(t *testing.T) {
	opts, err := parseInitArgs([]string{"--no-skills"}, "")
	if err != nil {
		t.Fatalf("parseInitArgs: %v", err)
	}
	if !opts.noSkills {
		t.Error("--no-skills did not set noSkills")
	}
	if got := planSkillsPath(opts); got != "" {
		t.Errorf("planSkillsPath with --no-skills = %q, want empty", got)
	}
}

// Skills install even with --no-hooks: that pairing is the "no unattended
// writes, I'll ask when I want it" setup, which is precisely who needs them.
func TestPlanSkillsPath_IndependentOfHooks(t *testing.T) {
	opts, err := parseInitArgs([]string{"--no-hooks"}, "")
	if err != nil {
		t.Fatalf("parseInitArgs: %v", err)
	}
	got := planSkillsPath(opts)
	if got == "" {
		t.Fatal("--no-hooks suppressed the skills path")
	}
	if filepath.Base(got) != "skills" {
		t.Errorf("skills path = %q, want a .../skills directory", got)
	}
}

func TestSkillsDir_PairsWithSettings(t *testing.T) {
	got := skillsDir(filepath.Join("/home", "u", ".claude", "settings.json"))
	want := filepath.Join("/home", "u", ".claude", "skills")
	if got != want {
		t.Errorf("skillsDir = %q, want %q", got, want)
	}
}

// frontmatterField pulls a top-level "key: value" out of a YAML frontmatter
// block. Sufficient for the flat frontmatter a SKILL.md uses.
func frontmatterField(front, key string) string {
	for line := range strings.SplitSeq(front, "\n") {
		if after, ok := strings.CutPrefix(line, key+":"); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}
