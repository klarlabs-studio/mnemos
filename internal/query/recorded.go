package query

import (
	"time"

	"go.klarlabs.de/mnemos/internal/recorded"
)

// Recorded-as-of reads (ADR 0036).
//
// RecordedAsOf promises the answer the brain would have given at T. recordedAt
// swaps the engine's repositories for the views in internal/recorded, bounded
// at T, once per call, so every read the engine makes — ranking, admission,
// hop expansion, priming, contradictions, narratives — sees the store as it
// was, merges included. The belief browse and gRPC ListBeliefs read through
// the same views, so every surface answers a recorded-as-of read the same way.

// recordedAt returns a copy of e whose reads are bounded at t.
func (e Engine) recordedAt(t time.Time) Engine {
	t = t.UTC()
	claims, rels := e.claims, e.relationships
	e.claims = recorded.Claims{ClaimRepository: claims, Events: e.events, At: t}
	e.relationships = recorded.Relationships{RelationshipRepository: rels, Claims: claims, At: t}
	e.events = recorded.Events{EventRepository: e.events, At: t}
	return e
}

// isTimeTravel reports whether opts read the past on either time axis. Such a
// read must not apply retrieval write-backs (ADR 0036 §2).
func isTimeTravel(opts AnswerOptions) bool {
	return !opts.AsOf.IsZero() || !opts.RecordedAsOf.IsZero()
}
