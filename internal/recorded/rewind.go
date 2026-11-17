// Package recorded reconstructs the store as it was recorded at an instant T
// (ADR 0035): what the brain believed then, not which of today's beliefs
// existed then.
//
// Filtering today's claims by created_at does not deliver that: status, valid
// time, the archive axis, merges, the relationship graph and the event corpus
// are all today's. This package wraps the repositories in views bounded at T.
// Every surface that reads the past — recall (internal/query), the belief
// browse (internal/browse, REST /v1/beliefs) and gRPC ListBeliefs — goes
// through these views, so a recorded-as-of read answers the same way
// everywhere. There is one rewind and many callers, deliberately.
//
// What cannot be reconstructed (trust, text, history older than its table, and
// the merge approximations described on Claims and Relationships) is listed in
// the ADR; it is current, not approximated silently.
package recorded

import (
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Rewind returns c as the store held it at t, and false when c had not been
// recorded yet. status and validity are c's full histories, oldest first.
//
// For each versioned field the value at t is the to-value of the last change at
// or before t; when every change is after t it is the from-value of the first;
// with no history it is the stored value, the only evidence there is.
//
// A merge redirect (MergedInto) is undone when the merge happened after t: the
// transition that carries domain.MergeRedirectReason dates it. Without that
// transition the stored pointer stands, like any field without history.
func Rewind(c domain.Claim, status []domain.ClaimStatusTransition, validity []domain.ValidityChange, t time.Time) (domain.Claim, bool) {
	if c.CreatedAt.After(t) {
		return domain.Claim{}, false
	}
	if s, ok := statusAt(status, t); ok {
		c.Status = s
	}
	if c.MergedInto != "" {
		if at, ok := MergedAt(status); ok && at.After(t) {
			c.MergedInto = ""
		}
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

// MergedAt returns when a claim became a merge redirect: the time of its last
// status transition with domain.MergeRedirectReason. False when it has none.
func MergedAt(status []domain.ClaimStatusTransition) (time.Time, bool) {
	var at time.Time
	found := false
	for _, s := range status {
		if s.Reason == domain.MergeRedirectReason && (!found || s.ChangedAt.After(at)) {
			at, found = s.ChangedAt, true
		}
	}
	return at, found
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
