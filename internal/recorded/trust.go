package recorded

import (
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/trust"
)

// Trust at T (ADR 0036 step 3).
//
// Canonical trust is trust.At over a domain.TrustInput (ADR 0026). A stored
// trust_score is that function evaluated over today's inputs, so a
// recorded-as-of read that returned it reported Thursday's corroboration and
// credit as Tuesday's belief. TrustInputAt rebuilds the inputs as they stood
// at T, and the views score them at T:
//
//	input           at T
//	evidence count  links recorded by T (LinkRecordedAt) to episodes ingested by
//	                T, graded by independence exactly as the stores grade them
//	                (domain.EffectiveEvidenceCount over distinct authors; an
//	                episode whose dependency on another of the claim's evidence
//	                episodes was recorded by T adds no author, ADR 0038 §3)
//	latest evidence the freshest such episode's timestamp
//	credit          the claim's credit history (CreditAt)
//	confidence,     the claim's trust-input history (StoredInputsAt): the
//	half-life,      values replaced by its first change after T, or the stored
//	last confirmed  values when nothing changed after T. A last confirmation
//	                still after T (possible only for a row confirmed again
//	                before the history existed) reads as none
//
// One rule covers rows written before the histories existed: a claim with no
// recorded change reads its stored values, held since creation as far as
// anything recorded says. That is exact for every claim not changed since, and
// for one changed since, the first recorded change carries the value it held
// before.

// LinkRecordedAt returns when the brain recorded an evidence link. A link
// carries its own time since ADR 0036 step 3. A link written before that has
// none, and the rule for it is the later of the claim's creation and the
// episode's ingestion: the link cannot predate either, and it is exact for the
// common case, a claim extracted from an episode as it was ingested, and for a
// restatement that attaches a new episode to an older claim. It is early only
// when extraction ran after both, which no recorded field dates.
func LinkRecordedAt(link domain.ClaimEvidence, claimCreated, eventIngested time.Time) time.Time {
	if !link.CreatedAt.IsZero() {
		return link.CreatedAt
	}
	if eventIngested.After(claimCreated) {
		return eventIngested
	}
	return claimCreated
}

// CreditAt returns the applied net credit a belief held at t: the to-value of
// the last change at or before t; when every change is after t, the from-value
// of the first; with no history, current, the only evidence there is (credit
// applied before credit history was recorded). history is oldest first.
func CreditAt(current float64, history []domain.CreditChange, t time.Time) float64 {
	if len(history) == 0 {
		return current
	}
	var last *domain.CreditChange
	for i := range history {
		if !history[i].ChangedAt.After(t) {
			last = &history[i]
		}
	}
	if last != nil {
		return last.To
	}
	return history[0].From
}

// StoredInputsAt returns the confidence, half-life and last confirmation c
// held at t: the prior values of the first change recorded after t, or c's
// stored values when nothing changed after t (including a row with no history
// at all). history is c's trust-input history; other claims' rows are ignored
// and order does not matter. A last confirmation after t is not one the brain
// had at t, so it reads as none.
func StoredInputsAt(c domain.Claim, history []domain.TrustInputChange, t time.Time) (confidence, halfLifeDays float64, lastConfirmed time.Time) {
	confidence, halfLifeDays, lastConfirmed = c.Confidence, c.HalfLifeDays, c.LastConfirmed
	var first *domain.TrustInputChange
	for i := range history {
		h := &history[i]
		if h.ClaimID != c.ID || !h.ChangedAt.After(t) {
			continue
		}
		if first == nil || h.ChangedAt.Before(first.ChangedAt) {
			first = h
		}
	}
	if first != nil {
		confidence, halfLifeDays, lastConfirmed = first.Confidence, first.HalfLifeDays, first.LastConfirmed
	}
	if lastConfirmed.After(t) {
		lastConfirmed = time.Time{}
	}
	return confidence, halfLifeDays, lastConfirmed
}

// TrustInputAt returns the trust inputs of c as they stood at t. links are c's
// evidence links (others are ignored), events the episodes they name (an
// episode missing from events, or ingested after t, is not evidence at t),
// credit c's credit history, oldest first, and inputs its trust-input history
// (StoredInputsAt). It is pure.
//
// deps are source dependencies between episodes (ADR 0038); only those
// recorded at or before t, between two of c's evidence episodes at t, count.
func TrustInputAt(c domain.Claim, links []domain.ClaimEvidence, events map[string]domain.Event, deps []domain.EventDependency, credit []domain.CreditChange, inputs []domain.TrustInputChange, t time.Time) domain.TrustInput {
	counted := map[string]bool{}
	var latest time.Time
	for _, l := range links {
		if l.ClaimID != c.ID || counted[l.EventID] {
			continue
		}
		ev, ok := events[l.EventID]
		if !ok || ev.IngestedAt.After(t) || LinkRecordedAt(l, c.CreatedAt, ev.IngestedAt).After(t) {
			continue
		}
		counted[l.EventID] = true
		if ev.Timestamp.After(latest) {
			latest = ev.Timestamp
		}
	}
	dependent := map[string]bool{}
	for _, d := range deps {
		if counted[d.EventID] && counted[d.DependsOn] && !d.CreatedAt.After(t) {
			dependent[d.EventID] = true
		}
	}
	authors := map[string]bool{}
	for id := range counted {
		if !dependent[id] {
			authors[events[id].CreatedBy] = true
		}
	}
	confidence, halfLife, confirmed := StoredInputsAt(c, inputs, t)
	var history []domain.CreditChange
	for _, ch := range credit {
		if ch.ClaimID == c.ID {
			history = append(history, ch)
		}
	}
	return domain.TrustInput{
		Confidence:     confidence,
		EvidenceCount:  domain.EffectiveEvidenceCount(len(authors), len(counted)),
		LatestEvidence: latest,
		LastConfirmed:  confirmed,
		HalfLifeDays:   halfLife,
		Credit:         CreditAt(domain.AppliedCredit(c.ConfidenceComponents), history, t),
	}
}

// TrustAt returns c's trust at t from its inputs at t: trust.At, the one trust
// function, over TrustInputAt.
func TrustAt(c domain.Claim, links []domain.ClaimEvidence, events map[string]domain.Event, deps []domain.EventDependency, credit []domain.CreditChange, inputs []domain.TrustInputChange, t time.Time) float64 {
	return trust.At(TrustInputAt(c, links, events, deps, credit, inputs, t), t)
}
