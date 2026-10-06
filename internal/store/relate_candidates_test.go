package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/relate"
	"go.klarlabs.de/mnemos/internal/store"
)

func candidateIDs(cs []domain.Claim) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

// oracle applies the candidate definition to every stored claim.
func oracle(t *testing.T, conn *store.Conn, q relate.CandidateQuery) []string {
	t.Helper()
	all, err := conn.Claims.ListAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	relate.SortCandidates(all)
	var ids []string
	for _, c := range all {
		if q.Matches(c) {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// A backend's RelateCandidates returns exactly the claims the definition
// accepts, in candidate order — after inserts, after a claim's text changes
// (its tokens must follow), and after a delete (its tokens must go).
func TestRelateCandidates_MatchTheDefinitionAcrossBackends(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	texts := []string{
		"The payments service retries failed webhooks",
		"Payment retries are capped at five attempts",
		"Deploying the search cluster requires approval",
		"Search latency increased after the index rebuild",
		"The billing team owns invoice generation",
		"Webhook delivery is not idempotent",
	}
	tested := 0
	for _, b := range openBackends(t) {
		src, ok := b.conn.Claims.(ports.RelateCandidateSource)
		if !ok {
			continue // falls back to ListAll; nothing to compare
		}
		tested++
		var claims []domain.Claim
		for i, txt := range texts {
			claims = append(claims, domain.Claim{ID: fmt.Sprintf("cl_%02d", i), Text: txt, Type: domain.ClaimTypeFact,
				Confidence: 0.7, Status: domain.ClaimStatusActive, CreatedAt: at.Add(time.Duration(i%2) * time.Hour)})
		}
		claims = append(claims, domain.Claim{ID: "cl_test", Text: "suite passed", Type: domain.ClaimTypeTestResult,
			TestID: "T-7", TestRequirementRef: "REQ-7", TestPassCount: 3, Confidence: 0.7, Status: domain.ClaimStatusActive, CreatedAt: at})
		if err := b.conn.Claims.Upsert(ctx, claims); err != nil {
			t.Fatalf("%s: %v", b.name, err)
		}

		check := func(label string, newText string) {
			t.Helper()
			q := relate.CandidateQueryFor([]domain.Claim{{ID: "cl_new", Text: newText}})
			got, err := src.RelateCandidates(ctx, q)
			if err != nil {
				t.Fatalf("%s %s: %v", b.name, label, err)
			}
			if want := oracle(t, b.conn, q); !slices.Equal(candidateIDs(got), want) {
				t.Errorf("%s %s: candidates %v, want %v", b.name, label, candidateIDs(got), want)
			}
		}
		check("shared tokens", "Webhook retries for the payments service")
		check("citation only", "unrelated words entirely, see cl_04")
		check("test results only", "zzz qqq")

		// Rewrite cl_00's text: its old tokens must stop matching.
		claims[0].Text = "Quarterly roadmap planning happens in October"
		if err := b.conn.Claims.Upsert(ctx, claims[:1]); err != nil {
			t.Fatal(err)
		}
		check("after text change (old tokens)", "payments service webhooks")
		check("after text change (new tokens)", "roadmap planning")

		if err := b.conn.Claims.DeleteCascade(ctx, "cl_05"); err != nil {
			t.Fatalf("%s: delete: %v", b.name, err)
		}
		check("after delete", "Webhook delivery idempotent")
	}
	if tested == 0 {
		t.Fatal("no backend implements RelateCandidateSource")
	}
}

// A brain whose token rows were written under another tokenizer is rebuilt on
// open, and one written before the index existed is backfilled.
func TestRelateCandidates_SQLiteRebuildsOnTokenizerChange(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tokens.db")
	dsn := "sqlite://" + path
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{{ID: "cl_a", Text: "ledger reconciliation runs nightly",
		Type: domain.ClaimTypeFact, Confidence: 0.7, Status: domain.ClaimStatusActive, CreatedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate rows from an older tokenizer: wrong tokens, stale version.
	if _, err := db.ExecContext(ctx, `DELETE FROM claim_tokens`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO claim_tokens(token, claim_id) VALUES ('bogus', 'cl_a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE relate_token_state SET tokenizer_version = 'relate-tokens/old'`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	conn, err = store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	q := relate.CandidateQueryFor([]domain.Claim{{ID: "cl_new", Text: "nightly ledger"}})
	got, err := conn.Claims.(ports.RelateCandidateSource).RelateCandidates(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(candidateIDs(got), []string{"cl_a"}) {
		t.Fatalf("after reopen: candidates %v, want [cl_a] (tokens not rebuilt)", candidateIDs(got))
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	var bogus int
	if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM claim_tokens WHERE token = 'bogus'`).Scan(&bogus); err != nil || bogus != 0 {
		t.Fatalf("stale token rows survived the rebuild (%d, %v)", bogus, err)
	}
}

// A build interrupted part-way (a hook killed at its timeout) resumes from its
// cursor on the next open rather than restarting or declaring itself done.
func TestRelateCandidates_SQLiteResumesAnInterruptedBuild(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "resume.db")
	dsn := "sqlite://" + path
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var claims []domain.Claim
	for i := 0; i < 30; i++ {
		claims = append(claims, domain.Claim{ID: fmt.Sprintf("cl_%03d", i), Text: fmt.Sprintf("ledger entry %d reconciles nightly", i),
			Type: domain.ClaimTypeFact, Confidence: 0.7, Status: domain.ClaimStatusActive, CreatedAt: time.Now()})
	}
	if err := conn.Claims.Upsert(ctx, claims); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	// Interrupted state: building under the current tokenizer, cursor at
	// cl_014, rows only for the claims at or before it.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DELETE FROM claim_tokens WHERE claim_id > 'cl_014'`,
		`UPDATE relate_token_state SET tokenizer_version = '', building_version = '` + relate.TokenizerVersion + `', build_cursor = 'cl_014'`,
	} {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	q := relate.CandidateQueryFor([]domain.Claim{{ID: "cl_new", Text: "nightly ledger"}})

	conn, err = store.Open(ctx, dsn) // resumes and completes the build
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	got, err := conn.Claims.(ports.RelateCandidateSource).RelateCandidates(ctx, q)
	if err != nil {
		t.Fatalf("after resume: %v", err)
	}
	if len(got) != 30 {
		t.Fatalf("after resume: %d candidates, want all 30 (claims after the cursor were not indexed)", len(got))
	}
	var n int
	if err := raw.QueryRowContext(ctx, `SELECT count(DISTINCT claim_id) FROM claim_tokens`).Scan(&n); err != nil || n != 30 {
		t.Fatalf("indexed claims = %d (%v), want 30", n, err)
	}
	_ = raw.Close()
}

// Another process can be mid-rebuild while this one has the brain open (a
// CLI upgrade next to a running server). The store must refuse to answer from
// the partial index rather than return a subset.
func TestRelateCandidates_SQLiteRefusesWhileAnotherProcessBuilds(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent.db")
	conn, err := store.Open(ctx, "sqlite://"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.ExecContext(ctx, `UPDATE relate_token_state SET tokenizer_version = '', building_version = 'relate-tokens/next', build_cursor = ''`); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Claims.(ports.RelateCandidateSource).RelateCandidates(ctx, relate.CandidateQueryFor([]domain.Claim{{Text: "anything"}}))
	if !errors.Is(err, ports.ErrRelateCandidatesNotReady) {
		t.Fatalf("RelateCandidates during another build = %v, want ErrRelateCandidatesNotReady", err)
	}
}
