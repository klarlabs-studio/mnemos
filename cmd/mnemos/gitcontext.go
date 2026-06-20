package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/relate"
)

const (
	defaultGitLogLimit  = 50
	maxGitLogLimit      = 1000
	gitLogFieldDelim    = "\x1f" // unit separator — won't appear in commit fields
	schemaVersionGitLog = "git.commit.v1"
)

type commitRecord struct {
	SHA         string
	AuthorName  string
	AuthorEmail string
	CommittedAt time.Time
	Subject     string
	Body        string
}

// repoIsGit reports whether root contains a .git directory or file
// (worktrees use a .git file pointing at the real gitdir).
func repoIsGit(root string) bool {
	info, err := os.Stat(filepath.Join(root, ".git"))
	if err != nil {
		return false
	}
	_ = info
	return true
}

// runGitLog invokes `git log` in repoRoot and returns the parsed commits,
// newest first. The format uses unit-separator delimiters and a NUL record
// terminator so commit bodies (which may contain newlines) round-trip
// cleanly. Returns an empty slice if git is unavailable or the repo is
// empty (rather than failing — git context is best-effort).
func runGitLog(ctx context.Context, repoRoot string, limit int, since string) ([]commitRecord, error) {
	if limit <= 0 {
		limit = defaultGitLogLimit
	}
	if limit > maxGitLogLimit {
		limit = maxGitLogLimit
	}

	args := []string{
		"-C", repoRoot,
		"log",
		"--no-color",
		"-n", strconv.Itoa(limit),
		"--pretty=format:%H" + gitLogFieldDelim +
			"%aN" + gitLogFieldDelim +
			"%aE" + gitLogFieldDelim +
			"%aI" + gitLogFieldDelim +
			"%s" + gitLogFieldDelim +
			"%b%x00",
	}
	if strings.TrimSpace(since) != "" {
		args = append(args, "--since="+strings.TrimSpace(since))
	}

	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // G204: git args are constructed from validated inputs
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Empty repo or no commits — git exits non-zero. Treat as no records.
			return nil, nil
		}
		return nil, fmt.Errorf("run git log: %w", err)
	}
	return parseGitLog(string(out))
}

func parseGitLog(out string) ([]commitRecord, error) {
	var commits []commitRecord
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	scanner.Split(splitOnNul)

	for scanner.Scan() {
		raw := strings.TrimLeft(scanner.Text(), "\n")
		if raw == "" {
			continue
		}
		fields := strings.SplitN(raw, gitLogFieldDelim, 6)
		if len(fields) < 6 {
			continue
		}
		ts, err := time.Parse(time.RFC3339, fields[3])
		if err != nil {
			continue
		}
		commits = append(commits, commitRecord{
			SHA:         strings.TrimSpace(fields[0]),
			AuthorName:  strings.TrimSpace(fields[1]),
			AuthorEmail: strings.TrimSpace(fields[2]),
			CommittedAt: ts,
			Subject:     strings.TrimSpace(fields[4]),
			Body:        strings.TrimSpace(fields[5]),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan git log: %w", err)
	}
	return commits, nil
}

func splitOnNul(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == 0 {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// existingGitCommitSHAs returns the set of commit SHAs already ingested into
// db (extracted from event metadata via JSON1).
func existingGitCommitSHAs(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	const q = `SELECT DISTINCT json_extract(metadata_json, '$.git_commit_sha') FROM events WHERE json_extract(metadata_json, '$.git_commit_sha') IS NOT NULL`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query commit SHAs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]struct{})
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scan SHA: %w", err)
		}
		if s.Valid && s.String != "" {
			out[s.String] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate SHAs: %w", err)
	}
	return out, nil
}

// ingestGitLog persists each commit as an event (deduped by SHA) and runs
// extract+relate so commit subjects/bodies become queryable claims. Returns
// counts and never fails fatally — per-commit errors are logged and
// skipped.
func ingestGitLog(ctx context.Context, w *govwrite.Writer, repoRoot string, limit int, since, actor string) (ingested, skipped int, err error) {
	conn := w.Conn()
	commits, err := runGitLog(ctx, repoRoot, limit, since)
	if err != nil {
		return 0, 0, err
	}
	if len(commits) == 0 {
		return 0, 0, nil
	}

	existing := map[string]struct{}{}
	if db, ok := conn.Raw.(*sql.DB); ok && db != nil {
		seen, sqlErr := existingGitCommitSHAs(ctx, db)
		if sqlErr != nil {
			fmt.Fprintf(os.Stderr, "git: failed to query existing SHAs: %v\n", sqlErr)
		} else {
			existing = seen
		}
	}

	extractor := extract.NewEngine()
	relEngine := relate.NewEngine()

	runID := fmt.Sprintf("git-log-%s", time.Now().UTC().Format("20060102T150405"))

	now := time.Now().UTC()
	var (
		newEvents []domain.Event
		newClaims []domain.Claim
		newLinks  []domain.ClaimEvidence
	)
	for _, c := range commits {
		if _, seen := existing[c.SHA]; seen {
			skipped++
			continue
		}
		event := buildCommitEvent(runID, c, now)
		claims, links, extractErr := extractor.Extract([]domain.Event{event})
		if extractErr != nil {
			fmt.Fprintf(os.Stderr, "git: extract %s: %v\n", c.SHA, extractErr)
			continue
		}
		newEvents = append(newEvents, event)
		newClaims = append(newClaims, claims...)
		newLinks = append(newLinks, links...)
		ingested++
	}

	if len(newEvents) == 0 {
		return ingested, skipped, nil
	}

	rels, relErr := relEngine.Detect(newClaims)
	if relErr != nil {
		fmt.Fprintf(os.Stderr, "git: detect relationships: %v\n", relErr)
		rels = nil
	}

	if existingClaims, listErr := conn.Claims.ListAll(ctx); listErr == nil && len(existingClaims) > 0 {
		if incremental, incErr := relEngine.DetectIncremental(newClaims, existingClaims); incErr == nil {
			rels = append(rels, incremental...)
		}
	}

	stampEventActor(newEvents, actor)
	stampClaimActor(newClaims, actor)
	stampRelationshipActor(rels, actor)
	if _, persistErr := w.Artifacts(ctx, newEvents, newClaims, newLinks, rels); persistErr != nil {
		return 0, skipped, fmt.Errorf("persist commits: %w", persistErr)
	}
	generateEmbeddingsBestEffort(ctx, conn, newEvents, newClaims)

	return ingested, skipped, nil
}

func buildCommitEvent(runID string, c commitRecord, ingestedAt time.Time) domain.Event {
	content := c.Subject
	if c.Body != "" {
		content = c.Subject + "\n\n" + c.Body
	}
	return domain.Event{
		ID:            "ev_git_" + c.SHA[:16],
		RunID:         runID,
		SchemaVersion: schemaVersionGitLog,
		Content:       content,
		SourceInputID: "git_" + c.SHA[:16],
		Timestamp:     c.CommittedAt.UTC(),
		Metadata: map[string]string{
			"source":             "git",
			"git_commit_sha":     c.SHA,
			"git_author_name":    c.AuthorName,
			"git_author_email":   c.AuthorEmail,
			"git_committed_at":   c.CommittedAt.UTC().Format(time.RFC3339),
			"git_commit_subject": c.Subject,
		},
		IngestedAt: ingestedAt,
	}
}
