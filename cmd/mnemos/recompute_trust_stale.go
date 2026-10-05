package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/trust"
)

// defaultTrustBackfillBatch bounds one rescore transaction. The full
// `recompute-trust --all` rewrites every row in one transaction (40 s on a
// 1M-belief baseline); a backfill instead commits and verifies in batches, so
// its cost per step never grows with the brain and an interrupted run resumes
// where it stopped — the rows it finished already carry the current version.
const defaultTrustBackfillBatch = 500

// trustBackfillPlan is what `recompute-trust --stale` would rescore.
type trustBackfillPlan struct {
	Total     int
	Stale     []string
	ByVersion map[string]int // stored model version ("" = never versioned) -> count
}

// planTrustBackfill selects every claim whose stored trust was not computed by
// the current model (ADR 0026 §5). A row with an empty version predates
// versioning, which means trust/v1: the global-time-constant formula.
func planTrustBackfill(claims []domain.Claim) trustBackfillPlan {
	p := trustBackfillPlan{Total: len(claims), ByVersion: map[string]int{}}
	for _, c := range claims {
		if c.TrustModelVersion == trust.ModelVersion {
			continue
		}
		p.Stale = append(p.Stale, c.ID)
		p.ByVersion[c.TrustModelVersion]++
	}
	return p
}

// applyTrustBackfill rescores ids in batches and READS EACH BATCH BACK: every
// row must come back stamped with the current model version and this pass's
// instant. "Wrote it and never persisted it" is exactly the failure a
// projection or migration slip produces, and a backfill that cannot see it
// would report success over an unchanged store.
func applyTrustBackfill(ctx context.Context, conn *store.Conn, ids []string, batch int, at time.Time) (rescored, batches int, err error) {
	scoped, ok := conn.Claims.(ports.ScopedTrustScorer)
	if !ok {
		return 0, 0, fmt.Errorf("backend %T cannot rescore a bounded set of claims; use recompute-trust --all", conn.Claims)
	}
	if batch <= 0 {
		batch = defaultTrustBackfillBatch
	}
	// Microsecond precision: Postgres and MySQL store no finer, and the
	// read-back compares what was written with what is stored.
	scoring := trust.Scorer(at.UTC().Truncate(time.Microsecond))
	for start := 0; start < len(ids); start += batch {
		chunk := ids[start:min(start+batch, len(ids))]
		n, err := scoped.RecomputeTrustForClaims(ctx, chunk, scoring)
		if err != nil {
			return rescored, batches, fmt.Errorf("rescore batch %d: %w", batches+1, err)
		}
		if err := verifyTrustBatch(ctx, conn, chunk, scoring); err != nil {
			return rescored, batches, fmt.Errorf("verify batch %d: %w", batches+1, err)
		}
		rescored += n
		batches++
	}
	return rescored, batches, nil
}

func verifyTrustBatch(ctx context.Context, conn *store.Conn, chunk []string, scoring domain.TrustScoring) error {
	got, err := conn.Claims.ListByIDs(ctx, chunk)
	if err != nil {
		return err
	}
	var bad []string
	for _, c := range got {
		if c.TrustModelVersion != scoring.ModelVersion || !c.TrustComputedAt.Equal(scoring.At) {
			bad = append(bad, c.ID)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("%d of %d rescored claim(s) did not persist the trust stamp (first: %s)", len(bad), len(chunk), bad[0])
	}
	return nil
}

func printTrustBackfillPlan(p trustBackfillPlan) {
	fmt.Printf("Trust model: %s\n", trust.ModelVersion)
	fmt.Printf("  claims:   %d\n", p.Total)
	fmt.Printf("  stale:    %d\n", len(p.Stale))
	versions := make([]string, 0, len(p.ByVersion))
	for v := range p.ByVersion {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	for _, v := range versions {
		label := v
		if label == "" {
			label = "(unversioned, trust/v1)"
		}
		fmt.Printf("    %-26s %d\n", label, p.ByVersion[v])
	}
}

// parseTrustBackfillBatch parses the --batch value.
func parseTrustBackfillBatch(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("--batch must be a positive integer, got %q", s)
	}
	return n, nil
}
