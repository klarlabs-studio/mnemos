package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// GlobalSchemaStatus is the lifecycle state of a promoted schema in the
// neocortex (global) tier. Under the operator gate a candidate is written
// Pending and requires explicit approval; under the auto gate it is written
// Active directly.
type GlobalSchemaStatus string

// Supported GlobalSchemaStatus values.
const (
	// GlobalSchemaStatusPending marks a promoted schema written under the
	// operator gate: it exists in the global tier but is NOT yet in force —
	// an operator must approve it before it participates in reads.
	GlobalSchemaStatusPending GlobalSchemaStatus = "pending"
	// GlobalSchemaStatusActive marks a promoted schema in force in the global
	// tier — written directly under the auto gate, or activated by an operator
	// approving a pending record.
	GlobalSchemaStatusActive GlobalSchemaStatus = "active"
)

// GlobalSchema is a de-identified generalization promoted from the per-tenant
// ("hippocampus") tier to the shared global ("neocortex") tier by the ADR 0011
// consolidation pass. It is the neocortex's first-class persistence unit.
//
// Its provenance is "corroborated by N distinct tenants" — COUNTS, not ids. A
// GlobalSchema deliberately carries NO tenant identifier, NO raw event text, and
// NO per-tenant evidence ids: only the generalized Statement plus aggregate
// magnitudes (DistinctTenants, EvidenceCount) and the ranking signals
// (Confidence, Surprise). This is why it is a distinct type rather than a
// [Schema]/[Lesson]: Schema.Validate requires ≥1 evidence id, which a promoted
// schema intentionally lacks — the corroboration count IS its provenance.
//
// Because it is the shared tier, a GlobalSchema is the INVERSE of the ADR-0007
// per-tenant isolation: it lives in the reserved default namespace and must be
// readable across every tenant, so its backing table is deliberately NOT
// tenant-scoped (no RLS / namespace partition).
type GlobalSchema struct {
	ID        string
	Statement string
	// Scope is the shared operational context (service/env/team) when every
	// corroborating tenant agreed on it; empty when it diverged.
	Scope Context
	// Polarity carries the positive/anti-schema sense of the generalization.
	Polarity SchemaPolarity
	// DistinctTenants is how many distinct tenants independently produced an
	// equivalent schema — the corroboration count that stands in for evidence.
	DistinctTenants int
	// EvidenceCount is the abstracted, aggregate count of corroborating pieces
	// of evidence across every contributing tenant. A magnitude, never a list.
	EvidenceCount int
	// Confidence is the representative confidence of the generalization, [0,1].
	Confidence float64
	// Surprise is the peak prediction-error (domain.Expectation) backing the
	// generalization; meaningful only when HasSurprise is set.
	Surprise    float64
	HasSurprise bool
	// Status is the neocortex lifecycle state (pending vs active).
	Status GlobalSchemaStatus
	// PromotedAt is when the consolidation pass wrote the record.
	PromotedAt time.Time
	CreatedBy  string
}

// Validate enforces the minimum invariants for persisting a promoted schema.
// Unlike [Schema], it requires NO evidence ids — the DistinctTenants
// corroboration count is the provenance. It does require that count to be ≥1 so
// a schema with no corroboration can never be persisted.
func (g GlobalSchema) Validate() error {
	if strings.TrimSpace(g.ID) == "" {
		return errors.New("global schema id is required")
	}
	if strings.TrimSpace(g.Statement) == "" {
		return errors.New("global schema statement is required")
	}
	if g.Confidence < 0 || g.Confidence > 1 {
		return fmt.Errorf("global schema confidence must be in [0, 1], got %v", g.Confidence)
	}
	if g.DistinctTenants < 1 {
		return fmt.Errorf("global schema distinct_tenants must be ≥ 1, got %d", g.DistinctTenants)
	}
	switch g.Status {
	case GlobalSchemaStatusPending, GlobalSchemaStatusActive:
	default:
		return fmt.Errorf("invalid global schema status %q", g.Status)
	}
	if g.PromotedAt.IsZero() {
		return errors.New("global schema promoted_at is required")
	}
	return nil
}
