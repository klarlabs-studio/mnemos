package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Doc-sync writes a repo's high-signal learnings into an agent-facing markdown
// file (AGENTS.md by default, or CLAUDE.md) so any LLM working in the repo picks
// them up natively — Claude Code and other agents auto-load these files. mnemos
// owns only a delimited block; everything the human wrote around it is
// preserved. The block is regenerated from the repo brain on `mnemos sync-docs`
// and after a repo-scoped session capture.

const (
	docBeginMarker = "<!-- mnemos:begin (generated — edits inside are re-synced into the brain) -->"
	docEndMarker   = "<!-- mnemos:end -->"
	// maxDocFacts caps the facts section so the agent file stays readable.
	maxDocFacts = 20
)

// upsertManagedBlock returns content with the mnemos-managed block (between the
// markers) inserted or replaced by block, preserving everything else. block is
// the inner content only (no markers). If no markers are present the block is
// appended; an empty file becomes just the block.
func upsertManagedBlock(existing, block string) string {
	wrapped := docBeginMarker + "\n" + strings.TrimRight(block, "\n") + "\n" + docEndMarker
	b := strings.Index(existing, docBeginMarker)
	e := strings.Index(existing, docEndMarker)
	if b >= 0 && e > b {
		return existing[:b] + wrapped + existing[e+len(docEndMarker):]
	}
	if strings.TrimSpace(existing) == "" {
		return wrapped + "\n"
	}
	return strings.TrimRight(existing, "\n") + "\n\n" + wrapped + "\n"
}

// generateRepoLearnings renders the managed-block markdown from the currently
// resolved brain: active decisions, top-trust facts, and open questions
// (hypotheses). Deprecated claims are omitted. repoRoot is used only to stamp
// the repo's tenant identity (git remote, else path) into the header so a clone
// can tie the committed doc back to its repo tenant. Returns the inner block.
func generateRepoLearnings(ctx context.Context, repoRoot string) (string, error) {
	conn, err := openConn(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()

	claims, err := conn.Claims.ListAll(ctx)
	if err != nil {
		return "", err
	}

	var decisions, facts, questions []domain.Claim
	for _, c := range claims {
		if c.Status == domain.ClaimStatusDeprecated {
			continue
		}
		switch c.Type {
		case domain.ClaimTypeDecision:
			decisions = append(decisions, c)
		case domain.ClaimTypeHypothesis:
			questions = append(questions, c)
		default:
			facts = append(facts, c)
		}
	}
	// Highest-trust facts first, capped.
	sort.SliceStable(facts, func(i, j int) bool { return facts[i].TrustScore > facts[j].TrustScore })
	if len(facts) > maxDocFacts {
		facts = facts[:maxDocFacts]
	}

	var b strings.Builder
	b.WriteString("## Repo learnings (mnemos)\n\n")
	b.WriteString("_Auto-maintained from this repository's mnemos brain. Follow these when working here; ")
	b.WriteString("you may edit inside the markers and mnemos will fold your changes back into the brain._\n")
	if tenant := repoTenantKey(repoRoot); tenant != "" {
		fmt.Fprintf(&b, "\n_Repo tenant: `%s`_\n", tenant)
	}

	writeSection := func(title string, cs []domain.Claim, showTrust bool) {
		if len(cs) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n### %s\n", title)
		for _, c := range cs {
			flag := ""
			if c.Status == domain.ClaimStatusContested {
				flag = " ⚠ contested"
			}
			text := strings.TrimSpace(c.Text)
			if showTrust && c.TrustScore > 0 {
				fmt.Fprintf(&b, "- %s (trust %.2f)%s\n", text, c.TrustScore, flag)
			} else {
				fmt.Fprintf(&b, "- %s%s\n", text, flag)
			}
		}
	}
	writeSection("Decisions", decisions, false)
	writeSection("Conventions & facts", facts, true)
	writeSection("Open questions", questions, false)

	if len(decisions)+len(facts)+len(questions) == 0 {
		b.WriteString("\n_(no durable learnings recorded yet)_\n")
	}
	return b.String(), nil
}

// syncRepoDocs regenerates the managed learnings block from the brain at
// brainDSN (empty = the resolved default) and writes it into <repoRoot>/fileName,
// preserving any human content. Returns the path and whether the file changed.
func syncRepoDocs(ctx context.Context, repoRoot, fileName, brainDSN string) (path string, changed bool, err error) {
	var block string
	gen := func() { block, err = generateRepoLearnings(ctx, repoRoot) }
	if brainDSN != "" {
		withBrainDSN(brainDSN, gen)
	} else {
		gen()
	}
	if err != nil {
		return "", false, err
	}

	path = filepath.Join(repoRoot, fileName)
	existing := ""
	if data, rerr := os.ReadFile(path); rerr == nil { //nolint:gosec // repo-local doc path
		existing = string(data)
	}
	updated := upsertManagedBlock(existing, block)
	if updated == existing {
		return path, false, nil
	}
	if werr := os.WriteFile(path, []byte(updated), 0o644); werr != nil { //nolint:gosec // agent-facing doc, world-readable by design
		return path, false, werr
	}
	return path, true, nil
}

// repoTenantKey returns a stable identity for the repo containing dir: the git
// remote URL (origin, else the first remote) when present — portable across
// clones and teammates — falling back to the repo root path when there is no
// remote. Empty when dir isn't inside a git repo. This is the federation key for
// repo-scoped knowledge (ADR 0007 tenant model); physical placement is separate.
func repoTenantKey(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	if remote := gitRemoteURL(dir); remote != "" {
		return remote
	}
	if root := gitTopLevel(dir); root != "" {
		return "path:" + root
	}
	return ""
}

func gitRemoteURL(dir string) string {
	if url := gitOutput(dir, "remote", "get-url", "origin"); url != "" {
		return url
	}
	// No origin: fall back to the first configured remote, if any.
	remotes := gitOutput(dir, "remote")
	if remotes == "" {
		return ""
	}
	first := strings.SplitN(strings.TrimSpace(remotes), "\n", 2)[0]
	return gitOutput(dir, "remote", "get-url", strings.TrimSpace(first))
}

func gitTopLevel(dir string) string {
	return gitOutput(dir, "rev-parse", "--show-toplevel")
}

// gitOutput runs git in dir and returns trimmed stdout, or "" on any error.
func gitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", args...) //nolint:gosec // fixed binary, fixed subcommands
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// handleSyncDocs implements `mnemos sync-docs` — write the repo's learnings into
// an agent-facing markdown file (AGENTS.md by default; --claude for CLAUDE.md,
// or --file <name>). Resolves the repo brain from the CWD so it works inside a
// repo regardless of a pinned global DSN.
func handleSyncDocs(args []string, f Flags) {
	fileName := "AGENTS.md"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--claude":
			fileName = "CLAUDE.md"
		case "--agents":
			fileName = "AGENTS.md"
		case "--file":
			if i+1 >= len(args) {
				exitWithMnemosError(f.Verbose, NewUserError("--file requires a filename"))
				return
			}
			fileName = args[i+1]
			i++
		default:
			exitWithMnemosError(f.Verbose, NewUserError("unknown sync-docs flag %q", args[i]))
			return
		}
	}

	// Prefer the repo brain (repo root from CWD); fall back to the resolved
	// default when not inside a repo brain.
	dbPath, repoRoot, ok := findProjectDB()
	if !ok {
		exitWithMnemosError(f.Verbose, NewUserError(
			"not inside a repo brain (no .mnemos/ found walking up) — run `mnemos init --project` here first"))
		return
	}
	brainDSN := "sqlite://" + dbPath

	path, changed, err := syncRepoDocs(context.Background(), repoRoot, fileName, brainDSN)
	if err != nil {
		exitWithMnemosError(f.Verbose, NewSystemError(err, "sync repo docs"))
		return
	}
	if changed {
		fmt.Printf("Updated %s with the repo's learnings (mnemos-managed block).\n", path)
	} else {
		fmt.Printf("%s already up to date.\n", path)
	}
}
