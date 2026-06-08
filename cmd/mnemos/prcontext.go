package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/relate"
	"go.klarlabs.de/mnemos/internal/store"
)

const (
	defaultGitPRLimit  = 20
	maxGitPRLimit      = 200
	schemaVersionGitPR = "git.pr.v1"
)

// ghAvailable reports whether the gh CLI is on PATH and authenticated for
// github.com. Both failures (missing binary, unauthed, network error) are
// treated identically: PR ingest is best-effort and skipped silently.
func ghAvailable(ctx context.Context) bool {
	if _, err := exec.LookPath("gh"); err != nil {
		return false
	}
	cmd := exec.CommandContext(ctx, "gh", "auth", "status", "--hostname", "github.com")
	cmd.Stderr = nil
	cmd.Stdout = nil
	return cmd.Run() == nil
}

type prRecord struct {
	Number   int       `json:"number"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	MergedAt time.Time `json:"mergedAt"`
	Author   struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	} `json:"author"`
}

// runGhPRs shells out to `gh pr list --state merged` in repoRoot and parses
// the result. Returns an empty slice on any recoverable failure (gh not
// installed, repo not on GitHub, not authenticated) so callers can treat PR
// ingestion as opportunistic.
func runGhPRs(ctx context.Context, repoRoot string, limit int) ([]prRecord, error) {
	if limit <= 0 {
		limit = defaultGitPRLimit
	}
	if limit > maxGitPRLimit {
		limit = maxGitPRLimit
	}

	cmd := exec.CommandContext(ctx, "gh",
		"pr", "list",
		"--state", "merged",
		"--limit", strconv.Itoa(limit),
		"--json", "number,title,body,mergedAt,author",
	)
	cmd.Dir = repoRoot
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Not a GitHub repo, or no PRs — not fatal.
			return nil, nil
		}
		return nil, fmt.Errorf("run gh pr list: %w", err)
	}

	var prs []prRecord
	if len(out) == 0 {
		return prs, nil
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parse gh pr list json: %w", err)
	}
	return prs, nil
}

// existingGitPRNumbers returns the set of PR numbers already ingested into
// db (extracted from event metadata via JSON1).
func existingGitPRNumbers(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	const q = `SELECT DISTINCT json_extract(metadata_json, '$.github_pr_number') FROM events WHERE json_extract(metadata_json, '$.github_pr_number') IS NOT NULL`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query PR numbers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]struct{})
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scan PR number: %w", err)
		}
		if s.Valid && s.String != "" {
			out[s.String] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate PR numbers: %w", err)
	}
	return out, nil
}

// ingestGhPRs persists each merged PR as an event (deduped by PR number)
// and runs extract+relate so PR bodies become queryable claims. Returns
// counts and never fails fatally — per-PR errors are logged and skipped.
func ingestGhPRs(ctx context.Context, conn *store.Conn, repoRoot string, limit int, actor string) (ingested, skipped int, err error) {
	prs, err := runGhPRs(ctx, repoRoot, limit)
	if err != nil {
		return 0, 0, err
	}
	if len(prs) == 0 {
		return 0, 0, nil
	}

	existing := map[string]struct{}{}
	if db, ok := conn.Raw.(*sql.DB); ok && db != nil {
		seen, sqlErr := existingGitPRNumbers(ctx, db)
		if sqlErr != nil {
			fmt.Fprintf(os.Stderr, "git-prs: failed to query existing PR numbers: %v\n", sqlErr)
		} else {
			existing = seen
		}
	}

	extractor := extract.NewEngine()
	relEngine := relate.NewEngine()

	runID := fmt.Sprintf("git-prs-%s", time.Now().UTC().Format("20060102T150405"))
	now := time.Now().UTC()

	var (
		newEvents []domain.Event
		newClaims []domain.Claim
		newLinks  []domain.ClaimEvidence
	)
	for _, p := range prs {
		numStr := strconv.Itoa(p.Number)
		if _, seen := existing[numStr]; seen {
			skipped++
			continue
		}
		event := buildPREvent(runID, p, now)
		claims, links, extractErr := extractor.Extract([]domain.Event{event})
		if extractErr != nil {
			fmt.Fprintf(os.Stderr, "git-prs: extract #%d: %v\n", p.Number, extractErr)
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
		fmt.Fprintf(os.Stderr, "git-prs: detect relationships: %v\n", relErr)
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
	if persistErr := pipeline.PersistArtifacts(ctx, conn, newEvents, newClaims, newLinks, rels); persistErr != nil {
		return 0, skipped, fmt.Errorf("persist PRs: %w", persistErr)
	}
	generateEmbeddingsBestEffort(ctx, conn, newEvents, newClaims)

	return ingested, skipped, nil
}

func buildPREvent(runID string, p prRecord, ingestedAt time.Time) domain.Event {
	content := p.Title
	if p.Body != "" {
		content = p.Title + "\n\n" + p.Body
	}
	numStr := strconv.Itoa(p.Number)
	return domain.Event{
		ID:            "ev_pr_" + numStr,
		RunID:         runID,
		SchemaVersion: schemaVersionGitPR,
		Content:       content,
		SourceInputID: "pr_" + numStr,
		Timestamp:     p.MergedAt.UTC(),
		Metadata: map[string]string{
			"source":              "github_pr",
			"github_pr_number":    numStr,
			"github_pr_title":     p.Title,
			"github_pr_author":    p.Author.Login,
			"github_pr_merged_at": p.MergedAt.UTC().Format(time.RFC3339),
		},
		IngestedAt: ingestedAt,
	}
}
