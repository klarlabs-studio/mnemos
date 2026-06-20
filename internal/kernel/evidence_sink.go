package kernel

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"go.klarlabs.de/axi/domain"
)

// evidenceSink persists every kernel domain event to a JSONL file on
// disk so cross-session audit isn't lost when bolt's stdout buffer
// rotates. The sink fans in behind [boltPublisher] — every event lands
// in both places. The JSONL path is off by default; opt in via the
// MNEMOS_AXI_EVIDENCE_LOG env var or the evidenceLogPath argument to
// [Build].
type evidenceSink struct {
	mu   sync.Mutex
	w    io.Writer
	c    io.Closer
	path string
}

// newEvidenceSink opens the JSONL evidence log at the given path
// (creating parent directories as needed). Empty path falls back to
// MNEMOS_AXI_EVIDENCE_LOG; still empty returns a nil sink — callers
// treat that as "disabled". Special values "-" / "stdout" route to
// os.Stdout.
func newEvidenceSink(path string) (*evidenceSink, error) {
	if path == "" {
		path = os.Getenv("MNEMOS_AXI_EVIDENCE_LOG")
	}
	if path == "" {
		return nil, nil
	}
	if path == "-" || path == "stdout" {
		return &evidenceSink{w: os.Stdout, path: "stdout"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("axi evidence log: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("axi evidence log: open %s: %w", path, err)
	}
	return &evidenceSink{w: f, c: f, path: path}, nil
}

// Publish writes one JSONL line per kernel event. Errors are silently
// dropped — losing one audit line shouldn't take down a write path. The
// bolt log already mirrors every event synchronously; the JSONL is a
// secondary index, not the audit-of-record.
func (s *evidenceSink) Publish(e domain.DomainEvent) {
	if s == nil {
		return
	}
	line := map[string]any{
		"event_type":  e.EventType(),
		"occurred_at": e.OccurredAt().UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
	}
	buf, err := json.Marshal(line)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.w.Write(buf)
	_, _ = s.w.Write([]byte{'\n'})
}

// Close flushes + closes the underlying file. Safe on a nil sink.
func (s *evidenceSink) Close() error {
	if s == nil || s.c == nil {
		return nil
	}
	return s.c.Close()
}
