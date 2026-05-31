package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felixgeelhaar/axi-go/domain"
)

// fakeDomainEvent satisfies the minimum domain.DomainEvent surface
// the sink reads. Wrapping a fake here keeps the test independent of
// axi-go's concrete event types.
type fakeDomainEvent struct {
	kind string
	at   time.Time
}

func (e fakeDomainEvent) EventType() string     { return e.kind }
func (e fakeDomainEvent) OccurredAt() time.Time { return e.at }

// TestAxiEvidenceSink_WritesJSONL covers the happy path: configured
// sink writes one JSONL line per event with parseable JSON.
func TestAxiEvidenceSink_WritesJSONL(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "axi-evidence.jsonl")
	sink, err := newAxiEvidenceSink(path)
	if err != nil {
		t.Fatalf("newAxiEvidenceSink: %v", err)
	}
	if sink == nil {
		t.Fatal("sink should be non-nil for explicit path")
	}
	t.Cleanup(func() { _ = sink.Close() })

	now := time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC)
	for i, kind := range []string{"session.started", "action.executed", "session.completed"} {
		sink.Publish(fakeDomainEvent{kind: kind, at: now.Add(time.Duration(i) * time.Second)})
	}
	_ = sink.Close()

	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3 (raw=%q)", len(lines), buf)
	}
	for i, line := range lines {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Errorf("line %d not valid JSON: %v (%q)", i, err, line)
		}
		if _, ok := parsed["event_type"].(string); !ok {
			t.Errorf("line %d missing event_type: %q", i, line)
		}
		if _, ok := parsed["occurred_at"].(string); !ok {
			t.Errorf("line %d missing occurred_at: %q", i, line)
		}
	}
}

// TestAxiEvidenceSink_EmptyPathDisables verifies the sink is
// off-by-default. Empty path + unset MNEMOS_AXI_EVIDENCE_LOG returns
// nil; callers treat that as "no sink".
func TestAxiEvidenceSink_EmptyPathDisables(t *testing.T) {
	t.Setenv("MNEMOS_AXI_EVIDENCE_LOG", "")
	sink, err := newAxiEvidenceSink("")
	if err != nil {
		t.Fatalf("newAxiEvidenceSink: %v", err)
	}
	if sink != nil {
		t.Fatalf("expected nil sink, got %+v", sink)
	}
	// Publishing on a nil sink must not panic.
	sink.Publish(fakeDomainEvent{kind: "x", at: time.Now()})
	_ = sink.Close()
}

// TestAxiEvidenceSink_EnvPath picks up the path from
// MNEMOS_AXI_EVIDENCE_LOG when the explicit arg is empty.
func TestAxiEvidenceSink_EnvPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "via-env.jsonl")
	t.Setenv("MNEMOS_AXI_EVIDENCE_LOG", path)
	sink, err := newAxiEvidenceSink("")
	if err != nil {
		t.Fatalf("newAxiEvidenceSink: %v", err)
	}
	if sink == nil {
		t.Fatal("expected non-nil sink with env path set")
	}
	t.Cleanup(func() { _ = sink.Close() })

	sink.Publish(fakeDomainEvent{kind: "test", at: time.Now()})
	_ = sink.Close()

	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Size() == 0 {
		t.Fatal("evidence log empty after publish")
	}
}

// TestMultiAxiPublisher_FansOut verifies events reach every wrapped
// publisher in order, and that nil entries are tolerated.
func TestMultiAxiPublisher_FansOut(t *testing.T) {
	t.Parallel()
	var hits1, hits2 int
	p1 := publisherFunc(func(_ time.Time, _ string) { hits1++ })
	p2 := publisherFunc(func(_ time.Time, _ string) { hits2++ })

	pub := multiAxiPublisher{p1, nil, p2}
	for range 3 {
		pub.Publish(fakeDomainEvent{kind: "x", at: time.Now()})
	}
	if hits1 != 3 || hits2 != 3 {
		t.Errorf("hits = (%d, %d), want (3, 3)", hits1, hits2)
	}
}

// publisherFunc is a tiny adapter wrapping a closure into a
// domain.DomainEventPublisher.
type publisherFunc func(time.Time, string)

func (f publisherFunc) Publish(e domain.DomainEvent) {
	f(e.OccurredAt(), e.EventType())
}
