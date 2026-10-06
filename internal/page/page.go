// Package page is keyset pagination for the browse APIs (#382 Phase 5).
//
// Offset pagination re-read and re-sorted the whole table for every page, and
// shifted under concurrent writes: a belief recorded while a client paged
// pushed every later page down one, so the client saw one row twice. A cursor
// names the last row of the previous page instead, and the next page starts
// strictly after it in newest-first order: created_at descending, then id
// descending, so rows recorded in the same instant still have one order.
package page

import (
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Key is a position in newest-first order: the created_at and id of a row.
type Key struct {
	At time.Time
	ID string
}

// ErrBadCursor is returned for a cursor this server did not issue.
var ErrBadCursor = errors.New("invalid cursor")

// Encode renders k as an opaque, URL-safe cursor.
func (k Key) Encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(k.At.UTC().Format(time.RFC3339Nano) + "|" + k.ID))
}

// Decode parses a cursor produced by Encode.
func Decode(s string) (Key, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Key{}, ErrBadCursor
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return Key{}, ErrBadCursor
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return Key{}, ErrBadCursor
	}
	return Key{At: t, ID: id}, nil
}

// After reports whether the row (at, id) comes after k in newest-first order.
func (k Key) After(at time.Time, id string) bool {
	return at.Before(k.At) || (at.Equal(k.At) && id < k.ID)
}

// Newer is the newest-first ordering, for sort.Slice and friends.
func Newer(aAt time.Time, aID string, bAt time.Time, bID string) bool {
	if !aAt.Equal(bAt) {
		return aAt.After(bAt)
	}
	return aID > bID
}

// ClaimFilter is the belief browse filter (GET /v1/beliefs).
type ClaimFilter struct {
	Type, Status string
	// AsOf keeps beliefs in force at that instant (domain.Belief.IsValidAt).
	AsOf time.Time
	// RecordedAsOf drops beliefs recorded after that instant.
	RecordedAsOf time.Time
	// RunID keeps beliefs with evidence from an event of that run.
	RunID string
}

// HasTimeBounds reports whether the filter compares times. A store whose
// times are text can only evaluate those comparisons approximately in SQL.
func (f ClaimFilter) HasTimeBounds() bool {
	return !f.AsOf.IsZero() || !f.RecordedAsOf.IsZero()
}

// Matches applies everything but RunID, which needs the evidence links.
func (f ClaimFilter) Matches(c domain.Claim) bool {
	if f.Type != "" && string(c.Type) != f.Type {
		return false
	}
	if f.Status != "" && string(c.Status) != f.Status {
		return false
	}
	if !f.AsOf.IsZero() && !c.IsValidAt(f.AsOf) {
		return false
	}
	if !f.RecordedAsOf.IsZero() && c.CreatedAt.After(f.RecordedAsOf) {
		return false
	}
	return true
}
