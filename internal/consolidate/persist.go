package consolidate

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// GlobalSchemaID derives a stable, content-addressed id for a promoted schema
// from its de-identified statement, scope, and polarity. Because it is
// deterministic, re-running a promotion pass upserts the SAME global record
// rather than churning a new one — so corroboration counts and confidence
// ratchet forward on the existing row. It contains no tenant-derived input.
func GlobalSchemaID(p PromotedLesson) string {
	h := sha256.Sum256([]byte(p.Statement + "\x00" + p.Scope.Key() + "\x00" + string(normPolarity(p.Polarity))))
	return "gsch_" + hex.EncodeToString(h[:12])
}

// ToGlobalSchema converts a de-identified [PromotedLesson] into a persistable
// [domain.GlobalSchema]. This is the ONLY bridge from a promotion candidate into
// the global store, so it is where the no-leak guarantee is enforced on the
// write path: only the aggregate, de-identified fields cross — Statement, Scope,
// Polarity, the corroboration/evidence COUNTS, Confidence, and Surprise.
// PromotedLesson structurally holds no tenant id and no raw evidence id, so there
// is nothing tenant-specific to copy.
func (p PromotedLesson) ToGlobalSchema(status domain.GlobalSchemaStatus, promotedAt time.Time, createdBy string) domain.GlobalSchema {
	return domain.GlobalSchema{
		ID:              GlobalSchemaID(p),
		Statement:       p.Statement,
		Scope:           p.Scope,
		Polarity:        normPolarity(p.Polarity),
		DistinctTenants: p.DistinctTenants,
		EvidenceCount:   p.EvidenceCount,
		Confidence:      p.Confidence,
		Surprise:        p.Surprise,
		HasSurprise:     p.HasSurprise,
		Status:          status,
		PromotedAt:      promotedAt,
		CreatedBy:       createdBy,
	}
}
