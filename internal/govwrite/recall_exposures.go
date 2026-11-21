package govwrite

import (
	"context"
	"fmt"

	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"
)

// Recall exposures (ADR 0048).
//
// The recall hook records which beliefs it injected into an agent session's
// context, so an episode that session later produces for one of them is read
// as recall-seeded, not as independent corroboration. Recording is idempotent:
// the store keeps the first exposure of a (session, belief).

type recallExposuresInput struct{ Exposures []domain.RecallExposure }

type recallExposuresExecutor struct{ conn *store.Conn }

// Execute records the exposures and an evidence row.
func (e recallExposuresExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := payload[recallExposuresInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("%s: unexpected input %T", actionRecallExposures, input)
	}
	repo, ok := e.conn.Claims.(ports.RecallExposureRepository)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("%s: the store cannot record recall exposures", actionRecallExposures)
	}
	if err := repo.RecordRecallExposures(ctx, in.Exposures); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("record recall exposures: %w", err)
	}
	return axidomain.ExecutionResult{
		Data:    writeCount{Accepted: len(in.Exposures)},
		Summary: fmt.Sprintf("recorded %d recall exposure(s)", len(in.Exposures)),
	}, ev("mnemos.write.recall_exposures", map[string]any{
		"exposures": len(in.Exposures),
	}), nil
}

// RecallExposures records which beliefs recall showed to which agent session.
// Returns the number of exposures submitted (a repeat of a stored exposure is
// accepted and keeps the first).
func (w *Writer) RecallExposures(ctx context.Context, exposures []domain.RecallExposure) (int, error) {
	out, err := dispatch[writeCount](ctx, w, actionRecallExposures, recallExposuresInput{Exposures: exposures})
	return out.Accepted, err
}
