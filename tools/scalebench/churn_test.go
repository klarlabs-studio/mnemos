package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/scalebench"
)

// growth reports every changed table and, for history tables only, the
// change per operation.
func TestGrowth(t *testing.T) {
	before := map[string]int64{"claims": 10, "claim_status_history": 4, "events": 3}
	after := map[string]int64{"claims": 10, "claim_status_history": 14, "events": 8, "cognitive_journal": 2}
	g, per := growth(before, after, 5)
	if len(g) != 3 || g["claim_status_history"] != 10 || g["events"] != 5 || g["cognitive_journal"] != 2 {
		t.Fatalf("growth = %v", g)
	}
	if len(per) != 2 || per["claim_status_history"] != 2 || per["cognitive_journal"] != 0.4 {
		t.Fatalf("per op = %v", per)
	}
	if g, per := growth(before, before, 5); g != nil || per != nil {
		t.Fatalf("no change reported %v %v", g, per)
	}
	// A work queue is censused but not budgeted as history.
	if g, per := growth(map[string]int64{"pending_references": 0}, map[string]int64{"pending_references": 40}, 5); g["pending_references"] != 40 || per != nil {
		t.Fatalf("pending_references growth = %v, per op = %v; want censused, not budgeted", g, per)
	}
}

// A read that grows a table or commits is a defect; a recall may commit (its
// write-backs update rows in place) but may not grow a table; a write over
// the per-operation budget is flagged; maintenance passes are not budgeted.
func TestFindings(t *testing.T) {
	phases := []churnPhase{
		{Name: "get", Kind: "read", Ops: 10, Committed: true},
		{Name: "health", Kind: "read", Ops: 1, Growth: map[string]int64{"cognitive_journal": 1}},
		{Name: "recall_ok", Kind: "recall", Ops: 10, Committed: true},
		{Name: "recall_bad", Kind: "recall", Ops: 10, Committed: true, Growth: map[string]int64{"claim_trust_input_history": 30}},
		{Name: "fat_write", Kind: "write", Ops: 2, PerOp: map[string]float64{"claim_status_history": 26}},
		{Name: "lean_write", Kind: "write", Ops: 2, PerOp: map[string]float64{"claim_status_history": 1}},
		{Name: "consolidate", Kind: "maintenance", Ops: 1, PerOp: map[string]float64{"claim_status_history": 5000}},
		{Name: "broken", Kind: "write", Error: "boom"},
		{Name: "restate_dup", Kind: "write", Ops: 3, Growth: map[string]int64{"claims": 3}, mustNotGrow: []string{"claims"}},
		{Name: "restate_ok", Kind: "write", Ops: 3, Growth: map[string]int64{"claim_evidence": 3}, mustNotGrow: []string{"claims"}},
	}
	got := strings.Join(findings(phases), "\n")
	for _, want := range []string{"DEFECT get", "DEFECT health", "DEFECT recall_bad", "fat_write grows claim_status_history", "broken: phase failed: boom", "DEFECT restate_dup (3 ops) added 3 claims rows"} {
		if !strings.Contains(got, want) {
			t.Errorf("findings missing %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"recall_ok", "lean_write", "consolidate", "restate_ok"} {
		if strings.Contains(got, not) {
			t.Errorf("findings flagged %q:\n%s", not, got)
		}
	}
}

// pick spreads its choices, never repeats one, and offsets by salt.
func TestPick(t *testing.T) {
	ids := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	got := pick(ids, 4, 0)
	if strings.Join(got, "") != "aceg" {
		t.Fatalf("pick = %v", got)
	}
	if strings.Join(pick(ids, 4, 1), "") != "bdfh" {
		t.Fatalf("salted pick = %v", pick(ids, 4, 1))
	}
	if len(pick(ids, 20, 0)) != len(ids) || pick(nil, 3, 0) != nil {
		t.Fatal("pick did not clamp")
	}
}

// A report written before p99 and the storage section existed still decodes,
// and the gate still reads a ceiling file that has no p99.
func TestReport_BackwardCompatible(t *testing.T) {
	old := `{"schema_version":"scalebench/v1","params":{"beliefs":1000,"shape":"uniform"},
		"operations":[{"name":"recall_hops1","samples":50,"p50_ms":8,"p95_ms":11,"max_ms":12,"outcome":"ok"}]}`
	var r Report
	if err := json.Unmarshal([]byte(old), &r); err != nil {
		t.Fatal(err)
	}
	if r.Storage != nil || r.Operations[0].P99MS != 0 {
		t.Fatalf("old report decoded with new fields set: %+v", r)
	}
	gate := writeGate(t, `{"beliefs":1000,"limits":{"uniform":{"recall_hops1":{"p95_ms":20}}}}`)
	if err := checkLimits(r, gate); err != nil {
		t.Fatalf("old report against old gate: %v", err)
	}
}

// The census reads what the store holds: it counts the seeded rows, and a
// churn phase sees the rows a write added and none for a read.
func TestCensusAndPhases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.db")
	dsn := "sqlite://" + path
	t.Setenv("MNEMOS_DB_URL", dsn)
	p := scalebench.Params{Beliefs: 200, Seed: 1, Shape: scalebench.ShapeUniform, Anchor: scalebench.DefaultAnchor, RunID: "t"}
	lr, err := scalebench.Load(context.Background(), dsn, p, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := openCensus(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	s, err := c.census()
	if err != nil {
		t.Fatal(err)
	}
	if s.Beliefs != int64(lr.Stats.Claims) || s.Episodes != int64(lr.Stats.Events) || s.Tables["claim_evidence"].Rows != int64(lr.Stats.Evidence) {
		t.Fatalf("census %d beliefs %d episodes %d evidence, loaded %+v", s.Beliefs, s.Episodes, s.Tables["claim_evidence"].Rows, lr.Stats)
	}
	if s.DBBytes <= 0 || s.BytesPerBelief <= 0 {
		t.Fatalf("census sizes %+v", s)
	}

	mem, err := mnemos.New(mnemos.WithStorage(dsn), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mem.Close() }()
	in, err := c.churnInputs(p)
	if err != nil || len(in.active) == 0 {
		t.Fatalf("churn inputs: %v (%d active)", err, len(in.active))
	}
	pr := &phaseRunner{c: c}
	pr.run("get", "read", func(ctx context.Context) (int, string, error) {
		_, err := mem.Get(ctx, in.active[0])
		return 1, "", err
	})
	pr.run("deprecate", "write", func(ctx context.Context) (int, string, error) {
		_, err := mem.DeprecateClaim(ctx, in.active[1], "test")
		return 1, "", err
	})
	get, dep := pr.phases[0], pr.phases[1]
	if get.Error != "" || get.Committed || get.Growth != nil {
		t.Fatalf("read phase %+v", get)
	}
	if dep.Error != "" || !dep.Committed || dep.Growth["claim_status_history"] != 1 || dep.PerOp["claim_status_history"] != 1 {
		t.Fatalf("write phase %+v", dep)
	}
}
