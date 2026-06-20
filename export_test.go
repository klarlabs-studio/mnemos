package mnemos

import (
	"errors"

	axidomain "go.klarlabs.de/axi/domain"
)

// TamperEvidenceForTest corrupts the record at index i of the given
// WriteSession's underlying evidence chain by replacing its Value while
// preserving the stored hash, then returns the result of re-verifying
// the chain. A correctly tamper-evident chain returns a non-nil error.
//
// Exported only for the package's external tests; it reaches through the
// public WriteSession into the concrete adapter so tests can prove the
// chain genuinely detects mutation.
func TamperEvidenceForTest(ws WriteSession, i int) error {
	adapter, ok := ws.(*writeSession)
	if !ok || adapter == nil || adapter.session == nil {
		return errors.New("not a kernel-backed write session")
	}

	// Snapshot preserves each record's stored hash. Mutating Value
	// without touching the hash leaves an inconsistent record that
	// VerifyEvidenceChain must reject.
	snap := adapter.session.ToSnapshot()
	if i < 0 || i >= len(snap.Evidence) {
		return errors.New("tamper index out of range")
	}
	snap.Evidence[i].Value = map[string]any{"tampered": true}

	restored, err := axidomain.SessionFromSnapshot(snap)
	if err != nil {
		return err
	}
	return restored.VerifyEvidenceChain()
}
