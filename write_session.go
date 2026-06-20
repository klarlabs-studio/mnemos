package mnemos

import (
	axidomain "go.klarlabs.de/axi/domain"
)

// Evidence is one tamper-evident record in a [WriteSession]'s chain. It
// is the public projection of axi-go's evidence record: the structured
// facts about a write that an auditor can verify after the fact (claim /
// event ids, counts, and token usage when a model ran).
type Evidence struct {
	// Kind classifies the record, e.g. "mnemos.remember",
	// "mnemos.remember_claim", "mnemos.remember_event", or "llm".
	Kind string

	// Source names the origin of the record, e.g. "mnemos.library" or
	// the model name for "llm" records.
	Source string

	// Value carries the structured facts (ids, counts, flags).
	Value any

	// TokensUsed is the token spend attributed to this record. Zero on
	// records that didn't consume model tokens.
	TokensUsed int64
}

// WriteSession is the public, read-only view of the axi-go execution
// session that governed a single Mnemos write. Every public write
// (Remember, RememberClaim, RememberEvent) runs through the kernel and
// records one such session, retrievable via [Memory.LastWriteSession].
//
// It exposes the session identifier, the evidence chain produced by the
// write, and a verifier that re-hashes the chain to detect tampering.
type WriteSession interface {
	// ID is the stable identifier of the underlying axi session.
	ID() string

	// Evidence returns the append-only evidence chain recorded for the
	// write, projected into the public [Evidence] shape.
	Evidence() []Evidence

	// VerifyEvidenceChain re-hashes the recorded evidence and returns a
	// non-nil error if any record has been tampered with. Delegates to
	// the axi-go session's chain verifier.
	VerifyEvidenceChain() error
}

// writeSession adapts an *axidomain.ExecutionSession into the public
// [WriteSession] surface. It holds the session value directly so the
// chain stays verifiable for the lifetime of the Memory handle.
type writeSession struct {
	session *axidomain.ExecutionSession
}

func newWriteSession(session *axidomain.ExecutionSession) *writeSession {
	return &writeSession{session: session}
}

func (w *writeSession) ID() string {
	if w == nil || w.session == nil {
		return ""
	}
	return string(w.session.ID())
}

func (w *writeSession) Evidence() []Evidence {
	if w == nil || w.session == nil {
		return nil
	}
	recs := w.session.Evidence()
	out := make([]Evidence, len(recs))
	for i, r := range recs {
		out[i] = Evidence{
			Kind:       r.Kind,
			Source:     r.Source,
			Value:      r.Value,
			TokensUsed: r.TokensUsed,
		}
	}
	return out
}

func (w *writeSession) VerifyEvidenceChain() error {
	if w == nil || w.session == nil {
		return nil
	}
	return w.session.VerifyEvidenceChain()
}

var _ WriteSession = (*writeSession)(nil)
