package domain

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// RecallExposure records that recall showed a belief to an agent session: the
// recall hook injected ClaimID into SessionID's context at ExposedAt (ADR 0048).
//
// It exists for one reason. A session that was shown a belief and then says it
// again is repeating what the brain told it, not reporting from an independent
// source, so an evidence episode of that session is recall-seeded and adds no
// corroboration (see [RecallSeeded]). Nothing else links a recall to a
// session: reconsolidation bumps a belief's last_verified, which says that
// something recalled it, never who.
//
// One row per (session, belief). The first exposure is the one kept: an
// episode is seeded when the session had seen the belief before saying it,
// and a later exposure never makes an earlier episode seeded.
type RecallExposure struct {
	SessionID string
	ClaimID   string
	ExposedAt time.Time
}

// Validate rejects an exposure that names no session or no belief, or has no
// instant: such a row could never be matched, or would match every episode.
func (x RecallExposure) Validate() error {
	if strings.TrimSpace(x.SessionID) == "" {
		return errors.New("recall exposure session id is required")
	}
	if strings.TrimSpace(x.ClaimID) == "" {
		return errors.New("recall exposure claim id is required")
	}
	if x.ExposedAt.IsZero() {
		return errors.New("recall exposure time is required")
	}
	return nil
}

// EvidenceSession is the session an evidence episode's ORIGIN names, or "" when
// its origin is not a session: a file path outranks the session it was read in
// ([EvidenceOrigin]), so a file read during a session is the file's evidence,
// never the session's, and recall cannot seed it.
func EvidenceSession(ev Event) string {
	if ev.Metadata[OriginPathKey] != "" {
		return ""
	}
	if s := ev.Metadata[OriginSessionKey]; s != "" {
		return s
	}
	return ev.Metadata[OriginLegacySessionKey]
}

// RecallSeeded reports whether ev, an evidence episode of claimID, was seeded
// by recall (ADR 0048): its origin is a session, and that session was shown
// claimID at or before the episode's timestamp. Such an episode is provenance,
// not corroboration: it is stored, listed and counted toward the total, and it
// adds no distinct source.
//
// The match is exact on (session, belief) and never by similarity (ADR 0038):
// the session must have been shown THIS belief, and the episode must support
// THIS belief. exposures may hold other sessions' and other beliefs' rows.
//
// The SQL backends restate this rule as a NOT EXISTS over recall_exposures and
// must agree with it; internal/store/trust_origin_parity_test.go checks that on
// every backend.
func RecallSeeded(ev Event, claimID string, exposures []RecallExposure) bool {
	session := EvidenceSession(ev)
	if session == "" {
		return false
	}
	for _, x := range exposures {
		if x.SessionID == session && x.ClaimID == claimID && !x.ExposedAt.After(ev.Timestamp) {
			return true
		}
	}
	return false
}

// SortRecallExposures orders exposures by session, then belief: the order
// every backend's ListRecallExposures returns.
func SortRecallExposures(xs []RecallExposure) {
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].SessionID != xs[j].SessionID {
			return xs[i].SessionID < xs[j].SessionID
		}
		return xs[i].ClaimID < xs[j].ClaimID
	})
}
