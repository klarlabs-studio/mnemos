package pipeline

import (
	"context"
	"errors"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/relate"
	"go.klarlabs.de/mnemos/internal/store"
)

// ExistingForRelate returns the stored claims DetectIncremental needs for
// newClaims. When the store can answer a relate.CandidateQuery it returns
// just the candidates; otherwise every claim, in candidate order. Both feed
// DetectIncremental the same relationships (relate's TestCandidateQuery_IsExact),
// so callers can switch to this from ListAll without changing what they relate.
//
// The write path used to call ListAll on every capture: 81% of a Remember on a
// 100k-claim brain was loading and decoding claims that could never pair with
// the new ones.
func ExistingForRelate(ctx context.Context, conn *store.Conn, newClaims []domain.Claim) ([]domain.Claim, error) {
	if src, ok := conn.Claims.(ports.RelateCandidateSource); ok {
		got, err := src.RelateCandidates(ctx, relate.CandidateQueryFor(newClaims))
		if !errors.Is(err, ports.ErrRelateCandidatesNotReady) {
			relate.TraceCandidateBudget(len(newClaims), len(got.Claims), got.SkippedTokens)
			return got.Claims, err
		}
		// Index still being built: the full corpus is slower, never wrong.
	}
	all, err := conn.Claims.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	relate.SortCandidates(all)
	return all, nil
}
