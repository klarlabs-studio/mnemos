package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Claude Code skill installation. Skills are the *manual* counterpart to the
// hooks: `hook brief` fires once at session start and `hook capture` fires
// once at session end, both unattended. The skills let the user ask for the
// same two things on demand — mid-session, after a /clear, or before a
// compaction — with the agent's in-window context available, which the
// unattended hooks never have.
//
// They are plain markdown, so unlike hooks they carry no DSN and no secret:
// they instruct the agent to use the MCP tools (which already know the brain)
// and fall back to the CLI only when MCP is unavailable.

//go:embed skills/*/SKILL.md
var skillFS embed.FS

// skillNames lists the skills we own, in install order. Membership here is
// also what makes uninstall/replace safe: we only ever overwrite these
// directories, never anything else under skills/.
//
// Both are prefixed `mnemos-` deliberately. Bare `brief` and `capture` are
// common skill names (the Agent OS memory skills use exactly those), and a
// collision would silently shadow one or the other.
var skillNames = []string{"mnemos-brief", "mnemos-capture"}

// skillsDir returns the Claude Code skills directory that pairs with a given
// settings.json — project-scoped or user-global, whichever init chose.
func skillsDir(settingsPath string) string {
	return filepath.Join(filepath.Dir(settingsPath), "skills")
}

// skillResult reports what installSkills did to one skill.
type skillResult struct {
	Name    string
	Path    string // the SKILL.md written
	Backup  string // prior version saved here; "" when it was newly created
	Changed bool   // false when the on-disk content already matched
}

// installSkills writes our SKILL.md files under dir/<name>/SKILL.md. It is
// idempotent: identical content is left alone (Changed=false) so re-running
// `mnemos init` does not churn files or spam backups. A differing prior
// version is backed up before being replaced, matching installHooks.
func installSkills(dir string) ([]skillResult, error) {
	results := make([]skillResult, 0, len(skillNames))
	for _, name := range skillNames {
		want, err := skillContent(name)
		if err != nil {
			return results, err
		}
		path := filepath.Join(dir, name, "SKILL.md")
		res := skillResult{Name: name, Path: path, Changed: true}

		if existing, readErr := os.ReadFile(path); readErr == nil { //nolint:gosec // operator's skills path
			if string(existing) == want {
				res.Changed = false
				results = append(results, res)
				continue
			}
			// A user may have edited ours, or another tool may own the name.
			// Either way the old content survives as .bak-mnemos.
			backup, bErr := backupFile(path)
			if bErr != nil {
				return results, fmt.Errorf("back up %s: %w", path, bErr)
			}
			res.Backup = backup
		} else if !os.IsNotExist(readErr) {
			return results, fmt.Errorf("read %s: %w", path, readErr)
		}

		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return results, err
		}
		if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
			return results, fmt.Errorf("write %s: %w", path, err)
		}
		results = append(results, res)
	}
	return results, nil
}

// skillContent returns the embedded SKILL.md body for a named skill.
func skillContent(name string) (string, error) {
	data, err := skillFS.ReadFile("skills/" + name + "/SKILL.md")
	if err != nil {
		return "", fmt.Errorf("embedded skill %q: %w", name, err)
	}
	return string(data), nil
}

// embeddedSkillNames lists the skills actually present in the embedded FS.
// Tests use it to catch a skill directory added without registering it in
// skillNames (it would ship in the binary but never install).
func embeddedSkillNames() []string {
	entries, err := fs.ReadDir(skillFS, "skills")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// skillSlashCommands renders the skills as the slash commands a user types,
// for init's preview and next-steps output.
func skillSlashCommands() string {
	cmds := make([]string, 0, len(skillNames))
	for _, n := range skillNames {
		cmds = append(cmds, "/"+n)
	}
	return strings.Join(cmds, ", ")
}
