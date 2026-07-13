package domain

import (
	"errors"
	"strings"
	"time"
)

// Journal kinds (ADR 0018). The cognitive journal is a single append-only log; kind
// discriminates what a row records.
const (
	// JournalKindConsolidation is one row per journaled consolidation pass: the full
	// ConsolidateResult plus a PredictiveError snapshot (the free-energy-over-time curve).
	JournalKindConsolidation = "consolidation"
	// JournalKindBeliefTrust is one row per belief whose trust a credit assignment moved
	// this pass: before / after / delta (the per-belief trust trajectory).
	JournalKindBeliefTrust = "belief_trust"
)

// JournalEntry is one append-only cognitive-journal record (ADR 0018) — a durable,
// timestamped note of what a learning mechanism did, so trajectories can be studied and
// constants tuned against real data. Data is a JSON payload whose shape depends on Kind;
// the store treats it as an opaque string, so new journal kinds are a code-only change.
type JournalEntry struct {
	// ID uniquely identifies the row.
	ID string
	// At is when the recorded event happened (the consolidation pass / credit application).
	At time.Time
	// RunID scopes the entry to an ingestion/execution run (empty = default).
	RunID string
	// Kind discriminates the payload (JournalKind*).
	Kind string
	// SubjectID is the claim id for a per-belief entry; empty for a pass-level entry.
	SubjectID string
	// Data is the kind-specific JSON payload.
	Data string
}

// Validate checks the invariants a journal row must satisfy before it is stored.
func (e JournalEntry) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return errors.New("journal entry id is required")
	}
	if strings.TrimSpace(e.Kind) == "" {
		return errors.New("journal entry kind is required")
	}
	if e.At.IsZero() {
		return errors.New("journal entry at is required")
	}
	return nil
}
