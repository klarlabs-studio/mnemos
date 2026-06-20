package kernel

import (
	"sync"

	"github.com/google/uuid"
	"go.klarlabs.de/axi/domain"
	"go.klarlabs.de/axi/inmemory"
)

// idGenerator mints collision-resistant session ids. axi's default
// generator is sequential ("session-1", "session-2", ...) which is fine
// for a single in-process kernel but reads poorly in a durable audit log
// and collides if two kernels ever share a log. We use UUIDv4 so every
// session id is globally unique, mirroring the event-id scheme.
type idGenerator struct{}

func (idGenerator) GenerateSessionID() domain.ExecutionSessionID {
	return domain.ExecutionSessionID("session-" + uuid.NewString())
}

// capturingSessionRepo wraps axi's in-memory session repository and
// records the most-recently-saved session. axi's ExecuteAction use case
// Save()s the session on every terminal path — including the hard-error
// path where Execute returns (nil, err) (executor-not-found, a
// ctx-cancel surfaced mid-execution). On that path the caller has no
// res.SessionID to look the session up by, yet the session exists and is
// auditable. last lets [Governed.LastSession] surface it so a failed
// write still leaves a retrievable trail.
//
// Capture is "whichever Save finished last", matching the documented
// concurrency contract for Memory.LastWriteSession (it reflects whichever
// write finished last). A single write is always correct; concurrent
// writers race only on which of their two sessions is "last", never on
// correctness of either session's own evidence chain.
type capturingSessionRepo struct {
	inner domain.SessionRepository

	// sink, when non-nil, receives every saved session so it can persist
	// the full evidence chain to the durable JSONL audit log. Save is the
	// only seam that carries the complete EvidenceRecord (Value, Source,
	// PreviousHash) — the domain-event stream does not.
	sink *evidenceSink

	mu   sync.RWMutex
	last *domain.ExecutionSession
}

func newCapturingSessionRepo() *capturingSessionRepo {
	return &capturingSessionRepo{inner: inmemory.NewExecutionSessionRepository()}
}

func (r *capturingSessionRepo) Save(session *domain.ExecutionSession) error {
	r.mu.Lock()
	r.last = session
	r.mu.Unlock()
	err := r.inner.Save(session)
	// Persist the durable audit after the in-memory save so a sink write
	// never blocks the canonical repository.
	r.sink.record(session)
	return err
}

func (r *capturingSessionRepo) Get(id domain.ExecutionSessionID) (*domain.ExecutionSession, error) {
	return r.inner.Get(id)
}

// lastSaved returns the most-recently-saved session, or nil if none has
// been saved yet.
func (r *capturingSessionRepo) lastSaved() *domain.ExecutionSession {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.last
}
