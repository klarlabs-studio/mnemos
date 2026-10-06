package sqlite

import (
	"context"

	"go.klarlabs.de/mnemos/internal/domain"
)

// inListChunk bounds the ids bound into one `IN (...)` list. SQLite refuses a
// statement with more than 32766 variables, and these lists were unbounded:
// a relate pass on a 100k-belief brain asks for ~50k candidate claims, the
// query failed, and the write path, which treats that read as best-effort,
// silently related the new claims against nothing. relationships bind each id
// twice, so the limit there was ~16k ids. Every *ByIDs read below goes through
// inChunks.
const inListChunk = 5000

// inChunks calls fn for each consecutive chunk of ids.
func inChunks(ids []string, fn func(chunk []string) error) error {
	for start := 0; start < len(ids); start += inListChunk {
		if err := fn(ids[start:min(start+inListChunk, len(ids))]); err != nil {
			return err
		}
	}
	return nil
}

// ListByIDs returns the stored claims among ids, in any number.
func (r ClaimRepository) ListByIDs(ctx context.Context, ids []string) ([]domain.Claim, error) {
	out := make([]domain.Claim, 0, len(ids))
	err := inChunks(ids, func(chunk []string) error {
		part, err := r.listByIDsChunk(ctx, chunk)
		out = append(out, part...)
		return err
	})
	return out, err
}

// ListByEventIDs returns the claims linked to any of eventIDs, each once even
// when its evidence spans chunks.
func (r ClaimRepository) ListByEventIDs(ctx context.Context, eventIDs []string) ([]domain.Claim, error) {
	out := []domain.Claim{}
	seen := map[string]bool{}
	err := inChunks(eventIDs, func(chunk []string) error {
		part, err := r.listByEventIDsChunk(ctx, chunk)
		for _, c := range part {
			if !seen[c.ID] {
				seen[c.ID] = true
				out = append(out, c)
			}
		}
		return err
	})
	return out, err
}

// ListEvidenceByClaimIDs returns the evidence links of claimIDs.
func (r ClaimRepository) ListEvidenceByClaimIDs(ctx context.Context, claimIDs []string) ([]domain.ClaimEvidence, error) {
	out := []domain.ClaimEvidence{}
	err := inChunks(claimIDs, func(chunk []string) error {
		part, err := r.listEvidenceByClaimIDsChunk(ctx, chunk)
		out = append(out, part...)
		return err
	})
	return out, err
}

// ListByIDs returns the stored events among ids, in any number.
func (r EventRepository) ListByIDs(ctx context.Context, ids []string) ([]domain.Event, error) {
	out := make([]domain.Event, 0, len(ids))
	err := inChunks(ids, func(chunk []string) error {
		part, err := r.listByIDsChunk(ctx, chunk)
		out = append(out, part...)
		return err
	})
	return out, err
}

// ListByClaimIDs returns every relationship touching any of claimIDs, each
// once: an edge whose endpoints fall in different chunks matches twice.
func (r RelationshipRepository) ListByClaimIDs(ctx context.Context, claimIDs []string) ([]domain.Relationship, error) {
	out := []domain.Relationship{}
	seen := map[string]bool{}
	err := inChunks(claimIDs, func(chunk []string) error {
		part, err := r.listByClaimIDsChunk(ctx, chunk)
		for _, rel := range part {
			if !seen[rel.ID] {
				seen[rel.ID] = true
				out = append(out, rel)
			}
		}
		return err
	})
	return out, err
}
