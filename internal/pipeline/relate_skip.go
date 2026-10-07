package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// relateSkips counts writes that stored new beliefs without relating them to
// the existing corpus (#428).
var relateSkips atomic.Int64

// RelateSkips returns how many writes in this process have stored new beliefs
// without relating them to existing ones. `mnemos serve` exports it as
// mnemos_relate_skipped_total.
func RelateSkips() int64 { return relateSkips.Load() }

// IncrementalDetector is the slice of the relate engine RelateToExisting needs.
type IncrementalDetector interface {
	DetectIncremental(newClaims, existing []domain.Claim) ([]domain.Relationship, error)
}

// RelateToExisting returns the edges between newClaims and the stored corpus.
//
// It is for write paths that keep the write when relating fails: a capture is
// still worth having without its edges. That makes a failure easy to lose,
// so this is the one place it is handled. When the corpus cannot be read or
// detection fails, the edges are skipped, the skip is logged as a warning and
// counted (RelateSkips), and the reason is returned for the caller to attach
// to its result. Callers that fail the write instead keep calling
// ExistingForRelate.
//
// Before #428 each write path dropped the error itself, so when #427 made the
// read fail on every brain above ~50k beliefs, every capture silently lost its
// supports and contradiction edges, with nothing in any log.
func RelateToExisting(ctx context.Context, conn *store.Conn, det IncrementalDetector, newClaims []domain.Claim, site string) ([]domain.Relationship, error) {
	if len(newClaims) == 0 {
		return nil, nil
	}
	existing, err := ExistingForRelate(ctx, conn, newClaims)
	if err != nil {
		return nil, SkipRelate(ctx, site, len(newClaims), fmt.Errorf("read existing claims: %w", err))
	}
	if len(existing) == 0 {
		return nil, nil
	}
	rels, err := det.DetectIncremental(newClaims, existing)
	if err != nil {
		return nil, SkipRelate(ctx, site, len(newClaims), fmt.Errorf("detect relationships: %w", err))
	}
	return rels, nil
}

// SkipRelate records that n new beliefs were kept without their edges to the
// existing corpus, and returns the reason wrapped for the caller's result.
func SkipRelate(ctx context.Context, site string, n int, cause error) error {
	relateSkips.Add(1)
	slog.WarnContext(ctx, "relate skipped: new beliefs stored without edges to existing ones",
		"site", site, "claims", n, "error", cause)
	return fmt.Errorf("relate skipped at %s: %w", site, cause)
}
