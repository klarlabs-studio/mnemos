package mnemos

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/kbmetrics"
)

// Metrics is the knowledge-base summary returned by [Memory.Metrics]: the
// same counts REST /v1/metrics, gRPC Metrics and the knowledge_metrics MCP tool
// report.
type Metrics struct {
	// Runs is the number of distinct runs with at least one event.
	Runs int64 `json:"runs"`
	// Events is the number of stored events.
	Events int64 `json:"events"`
	// Claims is the number of stored claims, every status included.
	Claims int64 `json:"claims"`
	// ContestedClaims is the number of claims in contested status.
	ContestedClaims int64 `json:"contested_claims"`
	// Relationships is the number of claim-to-claim edges.
	Relationships int64 `json:"relationships"`
	// Contradictions is the number of contradicts edges among them.
	Contradictions int64 `json:"contradictions"`
	// Embeddings is the number of stored event and claim vectors.
	Embeddings int64 `json:"embeddings"`
}

// Metrics implements [Memory.Metrics] through kbmetrics, the one
// implementation every transport shares.
func (m *memory) Metrics(ctx context.Context) (Metrics, error) {
	c, err := kbmetrics.Compute(ctx, m.conn, nil)
	if err != nil {
		return Metrics{}, fmt.Errorf("mnemos: Metrics: %w", err)
	}
	return Metrics{
		Runs: c.Runs, Events: c.Events, Claims: c.Claims, ContestedClaims: c.ContestedClaims,
		Relationships: c.Relationships, Contradictions: c.Contradictions, Embeddings: c.Embeddings,
	}, nil
}
