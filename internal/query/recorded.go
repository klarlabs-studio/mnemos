package query

import (
	"context"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// Recorded-as-of reads (ADR 0035).
//
// RecordedAsOf promises the answer the brain would have given at T. Filtering
// today's claims by created_at does not deliver it: status, valid time, the
// archive axis, the relationship graph and the event corpus are all today's.
// recordedAt therefore swaps the engine's repositories for views bounded at T,
// once per call, so every read the engine makes — ranking, admission, hop
// expansion, priming, contradictions, narratives — sees the store as it was.
// Admission then runs unchanged on the rewound claims.
//
// What the views cannot rewind (trust, merges, text) is listed in the ADR; it
// is current, not approximated.

// recordedAt returns a copy of e whose reads are bounded at t.
func (e Engine) recordedAt(t time.Time) Engine {
	t = t.UTC()
	e.events = recordedEvents{eventLister: e.events, at: t}
	e.claims = recordedClaims{ClaimRepository: e.claims, at: t}
	e.relationships = recordedRelationships{RelationshipRepository: e.relationships, at: t}
	return e
}

// isTimeTravel reports whether opts read the past on either time axis. Such a
// read must not apply retrieval write-backs (ADR 0035 §2).
func isTimeTravel(opts AnswerOptions) bool {
	return !opts.AsOf.IsZero() || !opts.RecordedAsOf.IsZero()
}

// rewindClaim returns c as the store held it at t, and false when c had not
// been recorded yet. status and validity are c's full histories, oldest first.
//
// For each versioned field the value at t is the to-value of the last change at
// or before t; when every change is after t it is the from-value of the first;
// with no history it is the stored value, the only evidence there is.
func rewindClaim(c domain.Claim, status []domain.ClaimStatusTransition, validity []domain.ValidityChange, t time.Time) (domain.Claim, bool) {
	if c.CreatedAt.After(t) {
		return domain.Claim{}, false
	}
	if s, ok := statusAt(status, t); ok {
		c.Status = s
	}
	var validTo, archived []domain.ValidityChange
	for _, v := range validity {
		switch v.Field {
		case domain.ValidityFieldValidTo:
			validTo = append(validTo, v)
		case domain.ValidityFieldArchivedAt:
			archived = append(archived, v)
		}
	}
	if v, ok := validityAt(validTo, t); ok {
		c.ValidTo = v
	}
	if v, ok := validityAt(archived, t); ok {
		c.ArchivedAt = v
	}
	return c, true
}

func statusAt(history []domain.ClaimStatusTransition, t time.Time) (domain.ClaimStatus, bool) {
	if len(history) == 0 {
		return "", false
	}
	var last *domain.ClaimStatusTransition
	for i := range history {
		if !history[i].ChangedAt.After(t) {
			last = &history[i]
		}
	}
	if last != nil {
		return last.ToStatus, last.ToStatus != ""
	}
	// Every transition is after t. An empty from-status is the insert row of a
	// claim recorded before its history began; it says nothing about t.
	first := history[0]
	return first.FromStatus, first.FromStatus != ""
}

func validityAt(history []domain.ValidityChange, t time.Time) (time.Time, bool) {
	if len(history) == 0 {
		return time.Time{}, false
	}
	var last *domain.ValidityChange
	for i := range history {
		if !history[i].ChangedAt.After(t) {
			last = &history[i]
		}
	}
	if last != nil {
		return last.To, true
	}
	return history[0].From, true
}

// recordedEvents hides events ingested after at.
type recordedEvents struct {
	eventLister
	at time.Time
}

func (r recordedEvents) keep(events []domain.Event, err error) ([]domain.Event, error) {
	if err != nil {
		return nil, err
	}
	out := events[:0:0]
	for _, ev := range events {
		if !ev.IngestedAt.After(r.at) {
			out = append(out, ev)
		}
	}
	return out, nil
}

// ListAll returns the events ingested at or before at.
func (r recordedEvents) ListAll(ctx context.Context) ([]domain.Event, error) {
	return r.keep(r.eventLister.ListAll(ctx))
}

// ListByIDs returns those of ids ingested at or before at.
func (r recordedEvents) ListByIDs(ctx context.Context, ids []string) ([]domain.Event, error) {
	return r.keep(r.eventLister.ListByIDs(ctx, ids))
}

// ListByRunID returns the run's events ingested at or before at.
func (r recordedEvents) ListByRunID(ctx context.Context, runID string) ([]domain.Event, error) {
	return r.keep(r.eventLister.ListByRunID(ctx, runID))
}

// recordedClaims returns claims rewound to at. Only the reads the engine makes
// are bounded; it never writes on this path (write-backs are skipped).
type recordedClaims struct {
	ports.ClaimRepository
	at time.Time
}

func (r recordedClaims) rewind(ctx context.Context, claims []domain.Claim, err error) ([]domain.Claim, error) {
	if err != nil {
		return nil, err
	}
	out := make([]domain.Claim, 0, len(claims))
	for _, c := range claims {
		if c.CreatedAt.After(r.at) {
			continue
		}
		status, err := r.ClaimRepository.ListStatusHistoryByClaimID(ctx, c.ID)
		if err != nil {
			return nil, fmt.Errorf("status history of %s: %w", c.ID, err)
		}
		validity, err := r.ListValidityHistory(ctx, c.ID)
		if err != nil {
			return nil, fmt.Errorf("validity history of %s: %w", c.ID, err)
		}
		if rc, ok := rewindClaim(c, status, validity, r.at); ok {
			out = append(out, rc)
		}
	}
	return out, nil
}

// ListByEventIDs returns the events' claims as recorded at at.
func (r recordedClaims) ListByEventIDs(ctx context.Context, eventIDs []string) ([]domain.Claim, error) {
	claims, err := r.ClaimRepository.ListByEventIDs(ctx, eventIDs)
	return r.rewind(ctx, claims, err)
}

// ListByIDs returns those of ids as recorded at at.
func (r recordedClaims) ListByIDs(ctx context.Context, ids []string) ([]domain.Claim, error) {
	claims, err := r.ClaimRepository.ListByIDs(ctx, ids)
	return r.rewind(ctx, claims, err)
}

// ListStatusHistoryByClaimID drops transitions after at, so lifecycle
// narratives end where the brain's knowledge did.
func (r recordedClaims) ListStatusHistoryByClaimID(ctx context.Context, claimID string) ([]domain.ClaimStatusTransition, error) {
	history, err := r.ClaimRepository.ListStatusHistoryByClaimID(ctx, claimID)
	if err != nil {
		return nil, err
	}
	out := history[:0:0]
	for _, h := range history {
		if !h.ChangedAt.After(r.at) {
			out = append(out, h)
		}
	}
	return out, nil
}

// recordedRelationships hides edges created after at.
type recordedRelationships struct {
	ports.RelationshipRepository
	at time.Time
}

func (r recordedRelationships) keep(rels []domain.Relationship, err error) ([]domain.Relationship, error) {
	if err != nil {
		return nil, err
	}
	out := rels[:0:0]
	for _, rel := range rels {
		if !rel.CreatedAt.After(r.at) {
			out = append(out, rel)
		}
	}
	return out, nil
}

// ListByClaimIDs returns the edges touching ids created at or before at.
func (r recordedRelationships) ListByClaimIDs(ctx context.Context, ids []string) ([]domain.Relationship, error) {
	return r.keep(r.RelationshipRepository.ListByClaimIDs(ctx, ids))
}

// ListByClaim returns the edges touching id created at or before at.
func (r recordedRelationships) ListByClaim(ctx context.Context, id string) ([]domain.Relationship, error) {
	return r.keep(r.RelationshipRepository.ListByClaim(ctx, id))
}

// ListAll returns the edges created at or before at.
func (r recordedRelationships) ListAll(ctx context.Context) ([]domain.Relationship, error) {
	return r.keep(r.RelationshipRepository.ListAll(ctx))
}
