package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/felixgeelhaar/axi-go/domain"
)

// axiEvidenceSink persists every kernel domain event to a JSONL file
// on disk so cross-session audit isn't lost when bolt's stdout
// buffer rotates. Adds the missing piece called out in the axi-go
// roadmap entry: "persist evidence chain to SQLite for cross-session
// audit". The JSONL flavour gets the same operator-grep ergonomics
// as the existing bolt log without touching every storage backend's
// schema; SQLite-backed persistence is a follow-up if a query
// surface beyond `jq` becomes worth it.
//
// The sink fans in behind [boltAxiPublisher] — every event lands in
// both places. Disabling either is independent (the JSONL path is
// off by default; opt in via [MNEMOS_AXI_EVIDENCE_LOG]).
type axiEvidenceSink struct {
	mu   sync.Mutex
	w    io.Writer
	c    io.Closer
	path string
}

// newAxiEvidenceSink opens the JSONL evidence log at the given path
// (creating parent directories as needed). Empty path returns a nil
// sink — callers treat that as "disabled".
//
// Path resolution: explicit `path` wins; otherwise the function
// reads MNEMOS_AXI_EVIDENCE_LOG. Special values "-" / "stdout" route
// to os.Stdout (useful in containers where stdout is already
// captured). Anything else is a filesystem path opened append-only.
func newAxiEvidenceSink(path string) (*axiEvidenceSink, error) {
	if path == "" {
		path = os.Getenv("MNEMOS_AXI_EVIDENCE_LOG")
	}
	if path == "" {
		return nil, nil
	}
	if path == "-" || path == "stdout" {
		return &axiEvidenceSink{w: os.Stdout, path: "stdout"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("axi evidence log: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("axi evidence log: open %s: %w", path, err)
	}
	return &axiEvidenceSink{w: f, c: f, path: path}, nil
}

// Publish writes one JSONL line per kernel event. Errors are
// silently dropped — losing one audit line shouldn't take down the
// MCP server. Operators with strict audit requirements watch the
// `jq 'select(.event_type == ...)'` tail and alert on gaps.
func (s *axiEvidenceSink) Publish(e domain.DomainEvent) {
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
	// fsync intentionally omitted: the bolt log already mirrors every
	// event synchronously; the JSONL is a secondary index, not the
	// audit-of-record. Operators who want fsync per line set
	// MNEMOS_AXI_EVIDENCE_LOG=stdout and let the container runtime
	// own durability.
}

// Close flushes + closes the underlying file. Safe on a nil sink.
func (s *axiEvidenceSink) Close() error {
	if s == nil || s.c == nil {
		return nil
	}
	return s.c.Close()
}

// multiAxiPublisher fans every kernel event into every wrapped
// publisher in declaration order. Used to compose the bolt logger
// with the optional JSONL evidence sink without forcing operators
// who don't enable the sink to pay for an extra hop.
type multiAxiPublisher []domain.DomainEventPublisher

// Publish satisfies domain.DomainEventPublisher.
func (m multiAxiPublisher) Publish(e domain.DomainEvent) {
	for _, p := range m {
		if p == nil {
			continue
		}
		p.Publish(e)
	}
}
