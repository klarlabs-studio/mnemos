package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/felixgeelhaar/mnemos/internal/domain"
	"github.com/felixgeelhaar/mnemos/internal/trust"
)

// handleTrust routes `mnemos trust --test=<requirement-ref>`.
// Surfaces every test_result claim under a requirement, ranks them by
// epistemic credibility (recency, pass-ratio, authority, citations),
// and reports the winner with rationale plus the runners-up so a human
// or agent can answer "which test should I trust?" without reading
// the underlying evidence row by row.
func handleTrust(args []string, _ Flags) {
	var testRef string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--test" || a == "--requirement":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("%s requires a value", a))
				return
			}
			testRef = args[i+1]
			i++
		default:
			exitWithMnemosError(false, NewUserError("unknown argument %q", a))
			return
		}
	}
	if testRef == "" {
		exitWithMnemosError(false, NewUserError("trust requires --test=<requirement-ref>"))
		return
	}

	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeConn(conn)

	all, err := conn.Claims.ListAll(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "list claims"))
		return
	}

	candidates := make([]domain.Claim, 0)
	for _, c := range all {
		if c.Type == domain.ClaimTypeTestResult && c.TestRequirementRef == testRef {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		emitJSON(map[string]any{
			"requirement_ref": testRef,
			"winner":          nil,
			"candidates":      []any{},
			"rationale":       "no test_result claims found for this requirement",
		})
		return
	}

	now := time.Now().UTC()
	type ranked struct {
		Claim     domain.Claim
		Score     float64
		Rationale string
	}
	out := make([]ranked, 0, len(candidates))
	for _, c := range candidates {
		score, rationale := trust.ScoreCredibility(trust.CredibilityInputs{
			CurrentTrust:    c.TrustScore,
			SourceAuthority: c.SourceAuthority,
			Liveness:        c.Liveness,
			CitationCount:   c.CitationCount,
			LastExecuted:    c.LastExecuted,
			LastVerified:    c.LastVerified,
			ValidFrom:       c.ValidFrom,
			CreatedAt:       c.CreatedAt,
			Now:             now,
			IsTest:          true,
			TestLastRunAt:   c.TestLastRunAt,
			TestPassCount:   c.TestPassCount,
			TestFailCount:   c.TestFailCount,
		})
		out = append(out, ranked{Claim: c, Score: score, Rationale: rationale})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })

	candidatesJSON := make([]map[string]any, 0, len(out))
	for _, r := range out {
		candidatesJSON = append(candidatesJSON, map[string]any{
			"claim_id":         r.Claim.ID,
			"text":             r.Claim.Text,
			"test_id":          r.Claim.TestID,
			"test_author":      r.Claim.TestAuthor,
			"test_last_run_at": formatTimeRFC3339(r.Claim.TestLastRunAt),
			"test_pass_count":  r.Claim.TestPassCount,
			"test_fail_count":  r.Claim.TestFailCount,
			"score":            r.Score,
			"rationale":        r.Rationale,
		})
	}

	winner := out[0]
	verdict := "winner"
	if len(out) > 1 {
		gap := out[0].Score - out[1].Score
		if gap < 0.05 {
			verdict = "ambiguous (top two within 0.05)"
		}
	}

	emitJSON(map[string]any{
		"requirement_ref": testRef,
		"verdict":         verdict,
		"winner": map[string]any{
			"claim_id":  winner.Claim.ID,
			"text":      winner.Claim.Text,
			"score":     winner.Score,
			"rationale": winner.Rationale,
		},
		"candidates": candidatesJSON,
	})
}

func formatTimeRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

var _ = fmt.Sprintf // keep fmt import for future expansion
