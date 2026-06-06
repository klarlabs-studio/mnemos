// Package autoedge wires the implicit relationships that fall out of
// recording outcomes and attaching them to decisions.
//
// Mnemos's classic relationships table is claim-only: it carries
// supports/contradicts edges between Claims. The polymorphic edges
// added in Phase 5+ (action_of, outcome_of, validates, refutes,
// derived_from) live in entity_relationships and connect actions,
// outcomes, claims, lessons, and playbooks across the graph. This
// package is the seam between domain operations (record an outcome,
// attach it to a decision) and the cross-entity edge bookkeeping the
// audit / replay layer needs.
package autoedge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// OnOutcomeAppended emits the action_of and outcome_of edges that
// connect an Outcome to its parent Action. Idempotent on the unique
// edge tuple, so re-running the helper is safe.
func OnOutcomeAppended(ctx context.Context, edges ports.EntityRelationshipRepository, outcome domain.Outcome, actor string) error {
	if outcome.ActionID == "" {
		return nil
	}
	now := time.Now().UTC()
	id1, err := newEdgeID()
	if err != nil {
		return err
	}
	id2, err := newEdgeID()
	if err != nil {
		return err
	}
	return edges.Upsert(ctx, []domain.EntityRelationship{
		{
			ID:        id1,
			Kind:      domain.RelationshipTypeActionOf,
			FromID:    outcome.ActionID,
			FromType:  domain.RelEntityAction,
			ToID:      outcome.ID,
			ToType:    domain.RelEntityOutcome,
			CreatedAt: now,
			CreatedBy: actor,
		},
		{
			ID:        id2,
			Kind:      domain.RelationshipTypeOutcomeOf,
			FromID:    outcome.ID,
			FromType:  domain.RelEntityOutcome,
			ToID:      outcome.ActionID,
			ToType:    domain.RelEntityAction,
			CreatedAt: now,
			CreatedBy: actor,
		},
	})
}

// OnDecisionOutcomeAttached fires validates or refutes edges from
// the outcome to each belief claim. Success outcomes validate the
// beliefs; failures refute them. Partial / unknown outcomes emit no
// edges (the audit layer can replay them through the Decision
// alone). Idempotent on the unique edge tuple.
func OnDecisionOutcomeAttached(
	ctx context.Context,
	decisions ports.DecisionRepository,
	outcomes ports.OutcomeRepository,
	edges ports.EntityRelationshipRepository,
	decisionID, outcomeID, actor string,
) error {
	if outcomeID == "" {
		return nil
	}
	outcome, err := outcomes.GetByID(ctx, outcomeID)
	if err != nil {
		return fmt.Errorf("autoedge: load outcome: %w", err)
	}
	beliefs, err := decisions.ListBeliefs(ctx, decisionID)
	if err != nil {
		return fmt.Errorf("autoedge: list beliefs: %w", err)
	}
	if len(beliefs) == 0 {
		return nil
	}
	var kind domain.RelationshipType
	switch outcome.Result {
	case domain.OutcomeResultSuccess:
		kind = domain.RelationshipTypeValidates
	case domain.OutcomeResultFailure:
		kind = domain.RelationshipTypeRefutes
	default:
		return nil
	}
	now := time.Now().UTC()
	emitted := make([]domain.EntityRelationship, 0, len(beliefs))
	for _, belief := range beliefs {
		id, err := newEdgeID()
		if err != nil {
			return err
		}
		emitted = append(emitted, domain.EntityRelationship{
			ID:        id,
			Kind:      kind,
			FromID:    outcome.ID,
			FromType:  domain.RelEntityOutcome,
			ToID:      belief,
			ToType:    domain.RelEntityClaim,
			CreatedAt: now,
			CreatedBy: actor,
		})
	}
	return edges.Upsert(ctx, emitted)
}

func newEdgeID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "er_" + hex.EncodeToString(buf), nil
}
