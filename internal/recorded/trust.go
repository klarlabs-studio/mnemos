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
//	                (domain.EffectiveEvidenceCount over distinct authors)
//	latest evidence the freshest such episode's timestamp
//	credit          the claim's credit history (CreditAt)
//	last confirmed  the stored value when it is at or before T, else none
//	confidence,     the stored values: not versioned on every backend, and the
//	half-life       pipeline does not rewrite them (ADR 0036 §3)
//
// last_confirmed keeps only the latest confirmation, so a belief confirmed both
// before and after T reads as unconfirmed at T. That understates its freshness
// at T; it never overstates it.

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

// TrustInputAt returns the trust inputs of c as they stood at t. links are c's
// evidence links (others are ignored), events the episodes they name (an
// episode missing from events, or ingested after t, is not evidence at t), and
// credit c's credit history, oldest first. It is pure.
func TrustInputAt(c domain.Claim, links []domain.ClaimEvidence, events map[string]domain.Event, credit []domain.CreditChange, t time.Time) domain.TrustInput {
	authors := map[string]bool{}
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
		authors[ev.CreatedBy] = true
		if ev.Timestamp.After(latest) {
			latest = ev.Timestamp
		}
	}
	var confirmed time.Time
	if !c.LastConfirmed.After(t) {
		confirmed = c.LastConfirmed
	}
	var history []domain.CreditChange
	for _, ch := range credit {
		if ch.ClaimID == c.ID {
			history = append(history, ch)
		}
	}
	return domain.TrustInput{
		Confidence:     c.Confidence,
		EvidenceCount:  domain.EffectiveEvidenceCount(len(authors), len(counted)),
		LatestEvidence: latest,
		LastConfirmed:  confirmed,
		HalfLifeDays:   c.HalfLifeDays,
		Credit:         CreditAt(domain.AppliedCredit(c.ConfidenceComponents), history, t),
	}
}

// TrustAt returns c's trust at t from its inputs at t: trust.At, the one trust
// function, over TrustInputAt.
func TrustAt(c domain.Claim, links []domain.ClaimEvidence, events map[string]domain.Event, credit []domain.CreditChange, t time.Time) float64 {
	return trust.At(TrustInputAt(c, links, events, credit, t), t)
}
