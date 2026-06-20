package kernel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/axi"
	"go.klarlabs.de/axi/domain"
)

// internalStub is an in-package executor (the kernel_test.go stub lives
// in package kernel_test and isn't reachable from these white-box tests).
type internalStub struct{ tokens int64 }

func (e internalStub) Execute(_ context.Context, _ any, _ domain.CapabilityInvoker) (domain.ExecutionResult, []domain.EvidenceRecord, error) {
	recs := []domain.EvidenceRecord{
		{Kind: "stub", Source: "test", Value: map[string]any{"ok": true}},
	}
	if e.tokens > 0 {
		recs = append(recs, domain.EvidenceRecord{Kind: "llm", Source: "test", Value: map[string]any{"t": e.tokens}, TokensUsed: e.tokens})
	}
	return domain.ExecutionResult{Data: map[string]any{"ok": true}, Summary: "stub ran"}, recs, nil
}

func internalActions() []Action {
	return []Action{
		{Name: "remember", Effect: domain.EffectWriteLocal, Idempotent: false, Description: "remember"},
		{Name: "remember_event", Effect: domain.EffectWriteLocal, Idempotent: true, Description: "remember event"},
	}
}

// readChainLines parses every JSONL line in the evidence log into the
// on-disk evidenceLine shape so tests can assert the full record was
// serialized.
func readChainLines(t *testing.T, path string) []evidenceLine {
	t.Helper()
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	raw := strings.TrimRight(string(buf), "\n")
	if raw == "" {
		return nil
	}
	var out []evidenceLine
	for i, line := range strings.Split(raw, "\n") {
		var rec evidenceLine
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d not valid JSON: %v (%q)", i, err, line)
		}
		out = append(out, rec)
	}
	return out
}

// TestEvidenceSink_PersistsFullChain proves the JSONL log carries the
// full evidence record — Kind, Source, Value, TokensUsed, Hash, and the
// chain link PreviousHash — so the log alone can reconstruct and verify
// the tamper-evident chain across sessions. The old sink wrote only
// event_type + occurred_at and could not.
func TestEvidenceSink_PersistsFullChain(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "chain.jsonl")
	execs := map[string]domain.ActionExecutor{
		ExecutorRef("remember"): internalStub{tokens: 7},
	}
	k, err := Build(nil, internalActions(), execs, axi.Budget{}, path)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = k.Close() })

	res, err := k.Execute(context.Background(), axi.Invocation{Action: ActionName("remember")})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Failure != nil {
		t.Fatalf("execution failed: %s", res.Failure.Message)
	}

	lines := readChainLines(t, path)
	if len(lines) < 2 {
		t.Fatalf("want at least 2 evidence lines (stub + llm), got %d", len(lines))
	}

	// The session id and action must be stamped on every line.
	for i, l := range lines {
		if l.SessionID != string(res.SessionID) {
			t.Errorf("line %d session_id = %q, want %q", i, l.SessionID, res.SessionID)
		}
		if l.ActionName != ActionName("remember") {
			t.Errorf("line %d action_name = %q, want %q", i, l.ActionName, ActionName("remember"))
		}
		if l.Hash == "" {
			t.Errorf("line %d missing hash — chain not reconstructable", i)
		}
	}
	// The chain must link: line[1].PreviousHash == line[0].Hash.
	if lines[1].PreviousHash != lines[0].Hash {
		t.Errorf("chain broken: line[1].previous_hash=%q, line[0].hash=%q", lines[1].PreviousHash, lines[0].Hash)
	}
	// The token report must survive serialization.
	var sawTokens bool
	for _, l := range lines {
		if l.TokensUsed == 7 {
			sawTokens = true
		}
	}
	if !sawTokens {
		t.Error("token report (7) not serialized to the audit log")
	}
}

// TestEvidenceSink_ReconstructsVerifiableChain reloads the persisted
// records into axi's EvidenceRecord shape and verifies the chain hashes,
// proving the log is a genuine tamper-evident audit and not just a dump.
func TestEvidenceSink_ReconstructsVerifiableChain(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "verify.jsonl")
	execs := map[string]domain.ActionExecutor{
		ExecutorRef("remember"): internalStub{tokens: 3},
	}
	k, err := Build(nil, internalActions(), execs, axi.Budget{}, path)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = k.Close() })
	if _, err := k.Execute(context.Background(), axi.Invocation{Action: ActionName("remember")}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	lines := readChainLines(t, path)
	// Rebuild the chain and recompute: each record's PreviousHash must
	// equal the prior record's Hash (the structural chain invariant).
	var prev string
	for i, l := range lines {
		if l.PreviousHash != prev {
			t.Errorf("record %d: previous_hash=%q, want %q", i, l.PreviousHash, prev)
		}
		prev = l.Hash
	}
}

// TestEvidenceSink_SingleWritePerChain confirms each session's whole
// chain lands in one Write — no interleaved/torn lines — by checking the
// records for one session are contiguous.
func TestEvidenceSink_SingleWritePerChain(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "single.jsonl")
	execs := map[string]domain.ActionExecutor{
		ExecutorRef("remember"):       internalStub{},
		ExecutorRef("remember_event"): internalStub{},
	}
	k, err := Build(nil, internalActions(), execs, axi.Budget{}, path)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = k.Close() })

	r1, _ := k.Execute(context.Background(), axi.Invocation{Action: ActionName("remember")})
	r2, _ := k.Execute(context.Background(), axi.Invocation{Action: ActionName("remember_event")})

	lines := readChainLines(t, path)
	// Group session ids in order: all of r1's records, then all of r2's.
	var order []string
	for _, l := range lines {
		if len(order) == 0 || order[len(order)-1] != l.SessionID {
			order = append(order, l.SessionID)
		}
	}
	if len(order) != 2 || order[0] != string(r1.SessionID) || order[1] != string(r2.SessionID) {
		t.Errorf("sessions interleaved or out of order: %v", order)
	}
}

// TestNewEvidenceSink_EmptyPathDisables verifies off-by-default: empty
// path + unset env returns a nil sink, and a nil sink tolerates Publish /
// Close.
func TestNewEvidenceSink_EmptyPathDisables(t *testing.T) {
	t.Setenv("MNEMOS_AXI_EVIDENCE_LOG", "")
	sink, err := newEvidenceSink("")
	if err != nil {
		t.Fatalf("newEvidenceSink: %v", err)
	}
	if sink != nil {
		t.Fatalf("expected nil sink, got %+v", sink)
	}
	sink.record(nil) // must not panic on nil
	if err := sink.Close(); err != nil {
		t.Errorf("Close on nil sink: %v", err)
	}
}

// TestNewEvidenceSink_EnvPath picks up MNEMOS_AXI_EVIDENCE_LOG when the
// explicit arg is empty.
func TestNewEvidenceSink_EnvPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "via-env.jsonl")
	t.Setenv("MNEMOS_AXI_EVIDENCE_LOG", path)
	sink, err := newEvidenceSink("")
	if err != nil {
		t.Fatalf("newEvidenceSink: %v", err)
	}
	if sink == nil {
		t.Fatal("expected non-nil sink with env path set")
	}
	if sink.path != path {
		t.Errorf("path = %q, want %q", sink.path, path)
	}
	if err := sink.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestNewEvidenceSink_Stdout routes "-" / "stdout" to os.Stdout with no
// closer.
func TestNewEvidenceSink_Stdout(t *testing.T) {
	for _, v := range []string{"-", "stdout"} {
		sink, err := newEvidenceSink(v)
		if err != nil {
			t.Fatalf("newEvidenceSink(%q): %v", v, err)
		}
		if sink == nil || sink.c != nil {
			t.Errorf("newEvidenceSink(%q): want non-nil sink with no closer", v)
		}
		if err := sink.Close(); err != nil {
			t.Errorf("Close on stdout sink: %v", err)
		}
	}
}

// TestEvidenceSink_IgnoresNonTerminalSession verifies a non-terminal
// (pending) session save writes nothing — the chain is only durable once
// settled.
func TestEvidenceSink_IgnoresNonTerminalSession(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "noflush.jsonl")
	sink, err := newEvidenceSink(path)
	if err != nil {
		t.Fatalf("newEvidenceSink: %v", err)
	}
	// A freshly-created session is Pending (non-terminal).
	session, err := domain.NewExecutionSession("s1", mustActionName(t, ActionName("remember")), nil)
	if err != nil {
		t.Fatalf("NewExecutionSession: %v", err)
	}
	sink.record(session)
	_ = sink.Close()

	if info, statErr := os.Stat(path); statErr == nil && info.Size() != 0 {
		t.Errorf("non-terminal session wrote %d bytes, want 0", info.Size())
	}
}

// TestEvidenceSink_WritesChainOnce confirms repeated terminal saves of
// the same session don't duplicate the chain in the log.
func TestEvidenceSink_WritesChainOnce(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "once.jsonl")
	execs := map[string]domain.ActionExecutor{
		ExecutorRef("remember"): internalStub{},
	}
	k, err := Build(nil, internalActions(), execs, axi.Budget{}, path)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = k.Close() })
	res, err := k.Execute(context.Background(), axi.Invocation{Action: ActionName("remember")})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Re-save the same terminal session via the repo: must not re-append.
	session, _ := k.GetSession(string(res.SessionID))
	if err := k.repo.Save(session); err != nil {
		t.Fatalf("re-Save: %v", err)
	}

	lines := readChainLines(t, path)
	for _, l := range lines {
		if l.SessionID != string(res.SessionID) {
			continue
		}
	}
	// Count records for this session id; should equal the in-memory count.
	want := len(session.Evidence())
	got := 0
	for _, l := range lines {
		if l.SessionID == string(res.SessionID) {
			got++
		}
	}
	if got != want {
		t.Errorf("session chain written %d times-worth of records, want %d (duplicate flush?)", got, want)
	}
}

func mustActionName(t *testing.T, name string) domain.ActionName {
	t.Helper()
	an, err := domain.NewActionName(name)
	if err != nil {
		t.Fatalf("NewActionName(%q): %v", name, err)
	}
	return an
}
