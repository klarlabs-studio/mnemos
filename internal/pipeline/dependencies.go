package pipeline

import (
	"context"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/provenance"
	"go.klarlabs.de/mnemos/internal/store"
)

// RecordEventDependencies detects and stores the source dependencies the given
// episodes declare (ADR 0038): citations, links and provenance metadata naming
// a stored source. Every write that stores episodes calls it after the
// episodes are appended (PersistArtifacts, the governed Events write and the
// library's direct episode write), so every ingest path records dependence
// the same way.
//
// A backend without a dependency store or a source lister records nothing:
// dependence is then invisible to retraction and trust, exactly as before
// ADR 0038. Every shipped backend has both.
func RecordEventDependencies(ctx context.Context, conn *store.Conn, events []domain.Event) error {
	if conn == nil || conn.Dependencies == nil || len(events) == 0 {
		return nil
	}
	lister, ok := conn.Events.(ports.EventSourceLister)
	if !ok {
		return nil
	}
	lookup := struct {
		ports.EventRepository
		ports.EventSourceLister
	}{conn.Events, lister}
	deps, err := provenance.Detect(ctx, lookup, events, time.Now().UTC(), domain.SystemUser)
	if err != nil {
		return fmt.Errorf("detect episode dependencies: %w", err)
	}
	if len(deps) == 0 {
		return nil
	}
	if err := conn.Dependencies.Append(ctx, deps); err != nil {
		return fmt.Errorf("record episode dependencies: %w", err)
	}
	return nil
}
