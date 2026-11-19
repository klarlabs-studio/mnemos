package brainbench

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/llm"
)

const reconcilePairsPath = "../../data/eval/reconciliation/pairs.yaml"

func TestReconcileSplit_IsAFunctionOfTheID(t *testing.T) {
	// Pinned: changing the split function would silently move pairs between
	// dev and test after results were seen.
	if got := ReconcileSplit("bb-limit"); got != SplitTest {
		t.Fatalf("bb-limit split = %s, want test", got)
	}
	if got := ReconcileSplit("rd-001"); got != SplitDev {
		t.Fatalf("rd-001 split = %s, want dev", got)
	}
	dev := 0
	const n = 2000
	for i := range n {
		if ReconcileSplit(fmt.Sprintf("pair-%d", i)) == SplitDev {
			dev++
		}
	}
	if share := float64(dev) / n; share < 0.2 || share > 0.4 {
		t.Errorf("dev share %.2f, want about 0.3", share)
	}
}

// The committed pair set is the experiment's ground truth: it must load, be
// large enough, and be balanced enough that precision and recall both mean
// something on the test split.
func TestReconcileSet_CommittedSetIsUsable(t *testing.T) {
	set, err := LoadReconcileSet(filepath.FromSlash(reconcilePairsPath))
	if err != nil {
		t.Fatal(err)
	}
	s := summarise(set)
	if s.Total < 150 {
		t.Errorf("%d pairs, want at least 150", s.Total)
	}
	for _, share := range []float64{float64(s.Contradicts) / float64(s.Total), float64(s.Compatible) / float64(s.Total)} {
		if share < 0.35 {
			t.Errorf("label share %.2f below 0.35: contradicts=%d compatible=%d", share, s.Contradicts, s.Compatible)
		}
	}
	if s.TestContradicts < 30 || s.Test-s.TestContradicts < 30 {
		t.Errorf("test split has %d contradicts and %d compatible; want 30 of each", s.TestContradicts, s.Test-s.TestContradicts)
	}
	if s.Hand == 0 || s.Hand == s.Total {
		t.Errorf("want both corpus-derived and hand-written pairs, got %d hand of %d", s.Hand, s.Total)
	}
}

func TestParseReconcileSet_RejectsMalformedPairs(t *testing.T) {
	good := "  - {id: x, a: one thing, b: another, label: compatible, category: c, source: hand, rationale: r}\n"
	cases := map[string]string{
		"bad label":     "  - {id: x, a: one, b: two, label: maybe, category: c, source: hand, rationale: r}\n",
		"duplicate id":  good + good,
		"empty side":    "  - {id: x, a: '', b: two, label: compatible, category: c, source: hand, rationale: r}\n",
		"no rationale":  "  - {id: x, a: one, b: two, label: compatible, category: c, source: hand}\n",
		"unknown field": "  - {id: x, a: one, b: two, label: compatible, category: c, source: hand, rationale: r, extra: 1}\n",
	}
	for name, body := range cases {
		if _, err := ParseReconcileSet([]byte("version: 1\npairs:\n" + body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseReconcileSet([]byte("version: 1\npairs:\n" + good)); err != nil {
		t.Errorf("good pair rejected: %v", err)
	}
}

func TestParseReconcileVerdict(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{`{"verdict": "contradicts"}`, LabelContradicts, true},
		{`{"verdict":"Compatible"}`, LabelCompatible, true},
		{"Sure. ```json\n{\"verdict\": \"contradicts\"}\n```", LabelContradicts, true},
		{"The statements are compatible.", LabelCompatible, true},
		{"These are incompatible claims", "", false}, // "incompatible" is not "compatible"
		{"contradicts? or compatible?", "", false},
		{`{"verdict": "unsure"}`, "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := ParseReconcileVerdict(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseReconcileVerdict(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestHybridRoute(t *testing.T) {
	cases := []struct {
		v    DetVerdict
		want string
	}{
		{DetVerdict{Contradicts: true, Strength: 0.9, SharesTopic: true}, RouteDeterministicEdge},
		{DetVerdict{Contradicts: true, Strength: HybridStrengthThreshold}, RouteDeterministicEdge},
		{DetVerdict{Contradicts: true, Strength: 0.6, SharesTopic: true}, RouteLowStrength},
		{DetVerdict{SharesTopic: true}, RouteTopicalNoEdge},
		{DetVerdict{}, RouteNoCandidate},
	}
	for _, tc := range cases {
		if got := HybridRoute(tc.v); got != tc.want {
			t.Errorf("HybridRoute(%+v) = %s, want %s", tc.v, got, tc.want)
		}
	}
	if RoutesToLLM(RouteDeterministicEdge) || RoutesToLLM(RouteNoCandidate) || !RoutesToLLM(RouteLowStrength) || !RoutesToLLM(RouteTopicalNoEdge) {
		t.Error("RoutesToLLM disagrees with the routing rule")
	}
}

func TestDeterministicVerdict_UsesTheProductionEngine(t *testing.T) {
	v, err := DeterministicVerdict("The payments API rate limit is one hundred requests per minute.",
		"The payments API rate limit is one thousand requests per minute.")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Contradicts || v.Detector != "numeric" || v.Strength <= 0 || !v.SharesTopic {
		t.Errorf("verdict = %+v, want a numeric contradiction on a topical pair", v)
	}
	v, err = DeterministicVerdict("The frontend uses React for rendering", "The backend uses Kafka for messaging")
	if err != nil {
		t.Fatal(err)
	}
	if v.Contradicts || v.SharesTopic {
		t.Errorf("verdict = %+v, want no edge and no topical overlap", v)
	}
}

// oracleJudge answers with the label (or a fixed verdict / error).
type oracleJudge struct {
	labels map[string]string // "a|b" -> label
	fixed  string
	err    bool
	calls  int
}

func (o *oracleJudge) Judge(_ context.Context, a, b string) Judgement {
	o.calls++
	if o.err {
		return Judgement{Err: "boom", LatencyMS: 1}
	}
	v := o.fixed
	if v == "" {
		v = o.labels[a+"|"+b]
	}
	return Judgement{Verdict: v, LatencyMS: 10, InputTokens: 100, OutputTokens: 5}
}

func smallSet(t *testing.T) ReconcileSet {
	t.Helper()
	set, err := ParseReconcileSet([]byte(`version: 1
pairs:
  - {id: n1, a: The payments API rate limit is one hundred requests per minute., b: The payments API rate limit is one thousand requests per minute., label: contradicts, category: numeric, source: s, rationale: r}
  - {id: n2, a: CI runs on Jenkins, b: CI runs on GitHub Actions, label: contradicts, category: tool, source: hand, rationale: r}
  - {id: n3, a: The frontend uses React for rendering, b: The backend uses Kafka for messaging, label: compatible, category: unrelated, source: s, rationale: r}
  - {id: n4, a: The team tracks work in Jira, b: The team switched its issue tracking to Linear, label: contradicts, category: tool, source: hand, rationale: r}
  - {id: n5, a: Revenue increased significantly in Q2, b: Revenue increased after the product launch in Q2, label: compatible, category: supports, source: s, rationale: r}
  - {id: n6, a: migration completed Tuesday, b: migration is still running, label: contradicts, category: temporal, source: s, rationale: r}
`))
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func oracleFor(set ReconcileSet) *oracleJudge {
	o := &oracleJudge{labels: map[string]string{}}
	for _, p := range set.Pairs {
		o.labels[p.A+"|"+p.B] = p.Label
	}
	return o
}

func TestRunReconcile_DeterministicOnlyReportsLLMArmsAsNotRun(t *testing.T) {
	rep, err := RunReconcile(context.Background(), smallSet(t), ReconcileOptions{BootstrapIters: 50, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if rep.LLMRun || len(rep.Arms) != 1 || rep.Arms[0].Arm != ArmDeterministic {
		t.Fatalf("want only the deterministic arm, got LLMRun=%v arms=%d", rep.LLMRun, len(rep.Arms))
	}
	if rep.Arms[0].CostAll.LLMCalls != 0 {
		t.Error("deterministic arm made LLM calls")
	}
	var out bytes.Buffer
	if err := rep.WriteHuman(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "NOT RUN") {
		t.Error("human report does not say the LLM arms were not run")
	}
}

func TestRunReconcile_ArmsAndCosts(t *testing.T) {
	set := smallSet(t)
	oracle := oracleFor(set)
	rep, err := RunReconcile(context.Background(), set, ReconcileOptions{
		Judges: []NamedJudge{{Model: "oracle", Judge: oracle}}, BootstrapIters: 200, Seed: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if oracle.calls != len(set.Pairs) {
		t.Errorf("judge called %d times, want one call per pair (%d): the hybrid arm must reuse the llm pass", oracle.calls, len(set.Pairs))
	}
	arms := map[string]ArmResult{}
	for _, a := range rep.Arms {
		arms[a.Arm] = a
	}
	if f := arms[ArmLLM].All.F1; f == nil || *f != 1 {
		t.Errorf("oracle llm arm F1 = %v, want 1", f)
	}

	routed := 0
	for _, o := range rep.Pairs {
		if RoutesToLLM(o.Route) {
			routed++
		}
	}
	if got := arms[ArmHybrid].CostAll.LLMCalls; got != routed {
		t.Errorf("hybrid made %d calls, want %d (the routed pairs)", got, routed)
	}
	if got := arms[ArmLLM].CostAll.LLMCalls; got != len(set.Pairs) {
		t.Errorf("llm arm made %d calls, want %d", got, len(set.Pairs))
	}
	if c := arms[ArmLLM].CostAll; !c.TokensReported || c.PromptTokens != 100*len(set.Pairs) || c.DeterministicMS != 0 {
		t.Errorf("llm arm cost = %+v", c)
	}
	if arms[ArmHybrid].DeltaF1 == nil {
		t.Error("hybrid arm has no paired delta against deterministic")
	}
	// An oracle hybrid can only fix the pairs it routes: it is never worse than
	// deterministic on any pair.
	if *arms[ArmHybrid].All.F1 < *arms[ArmDeterministic].All.F1 {
		t.Errorf("oracle hybrid F1 %v below deterministic %v", *arms[ArmHybrid].All.F1, *arms[ArmDeterministic].All.F1)
	}
	for _, a := range rep.Arms {
		for _, m := range []SplitMetrics{a.All} {
			if m.F1 != nil && m.F1CI != nil && (*m.F1 < m.F1CI.Lo-1e-9 || *m.F1 > m.F1CI.Hi+1e-9) && m.N > 3 {
				t.Logf("%s: F1 %.3f outside its CI %v (possible on tiny sets)", armName(a), *m.F1, *m.F1CI)
			}
		}
	}
}

func TestRunReconcile_FailedCallsAreCompatibleAndCounted(t *testing.T) {
	set := smallSet(t)
	rep, err := RunReconcile(context.Background(), set, ReconcileOptions{
		Judges: []NamedJudge{{Model: "down", Judge: &oracleJudge{err: true}}}, BootstrapIters: 20, Seed: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range rep.Arms {
		if a.Arm != ArmLLM {
			continue
		}
		if a.All.TP+a.All.FP != 0 {
			t.Errorf("failed calls produced %d edges", a.All.TP+a.All.FP)
		}
		if a.All.Precision != nil {
			t.Errorf("precision with no predicted positives = %v, want unknown", *a.All.Precision)
		}
		if a.CostAll.CallErrors != len(set.Pairs) {
			t.Errorf("call errors = %d, want %d", a.CostAll.CallErrors, len(set.Pairs))
		}
	}
}

func TestPRF_UndefinedIsNil(t *testing.T) {
	p, r, f := prf(0, 0, 0)
	if p != nil || r != nil || f != nil {
		t.Error("all-zero confusion must be unknown, not 0")
	}
	p, r, f = prf(3, 1, 2)
	if *p != 0.75 || *r != 0.6 || f == nil {
		t.Errorf("prf(3,1,2) = %v %v %v", *p, *r, *f)
	}
}

type fakeLLM struct {
	got  []llm.Message
	resp llm.Response
	err  error
}

func (f *fakeLLM) Complete(_ context.Context, m []llm.Message) (llm.Response, error) {
	f.got = m
	return f.resp, f.err
}

func TestLLMJudge_SendsTheVersionedPromptAndReadsTheReply(t *testing.T) {
	f := &fakeLLM{resp: llm.Response{Content: `{"verdict": "contradicts"}`, InputTokens: 210, OutputTokens: 7}}
	j := NewLLMJudge(f).Judge(context.Background(), "A side", "B side")
	if j.Verdict != LabelContradicts || j.InputTokens != 210 || j.OutputTokens != 7 || j.Err != "" {
		t.Errorf("judgement = %+v", j)
	}
	if len(f.got) != 2 || f.got[0].Content != ReconcileSystemPrompt || !strings.Contains(f.got[1].Content, "Statement A: A side\nStatement B: B side") {
		t.Errorf("messages = %+v", f.got)
	}
	if !strings.Contains(ReconcileSystemPrompt, "JSON") {
		t.Error("prompt must ask for JSON so the client enables JSON mode")
	}

	f = &fakeLLM{err: errors.New("connection refused")}
	j = NewLLMJudge(f).Judge(context.Background(), "a", "b")
	if j.Verdict != "" || j.Err == "" {
		t.Errorf("failed call judgement = %+v", j)
	}
}
