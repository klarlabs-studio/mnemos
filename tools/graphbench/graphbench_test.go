package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/scalebench"
)

// TestMain pins a throwaway DSN so nothing in this binary can fall back to
// the developer's real brain.
func TestMain(m *testing.M) {
	_ = os.Setenv("MNEMOS_DB_URL", "memory://")
	os.Exit(m.Run())
}

func testParams(n int) scalebench.Params {
	return scalebench.Params{Beliefs: n, Seed: 7, Shape: scalebench.ShapeUniform, Anchor: scalebench.DefaultAnchor, RunID: "graphbench"}
}

// The corpus is a pure function of its params, and the scalebench part of it
// is untouched apart from the merge redirects.
func TestGenerateCorpus_Deterministic(t *testing.T) {
	for _, profile := range []Profile{ProfileUniform, ProfileDense} {
		a, err := GenerateCorpus(testParams(3000), profile, 500)
		if err != nil {
			t.Fatal(err)
		}
		b, err := GenerateCorpus(testParams(3000), profile, 500)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a.Batches, b.Batches) {
			t.Fatalf("%s: two generations of the same params differ", profile)
		}
	}
}

func TestGenerateCorpus_OverlayPreservesScalebench(t *testing.T) {
	p := testParams(2000)
	c, err := GenerateCorpus(p, ProfileUniform, 500)
	if err != nil {
		t.Fatal(err)
	}
	g, err := scalebench.NewGenerator(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range c.Batches {
		want := g.Next(500)
		if !reflect.DeepEqual(b.Events, want.Events) || !reflect.DeepEqual(b.Evidence, want.Evidence) ||
			!reflect.DeepEqual(b.Relationships, want.Relationships) {
			t.Fatal("uniform profile changed scalebench's episodes, evidence or associations")
		}
		for i, cl := range b.Claims {
			if cl.MergedInto == "" && !reflect.DeepEqual(cl, want.Claims[i]) {
				t.Fatalf("belief %s changed although it is not a redirect", cl.ID)
			}
		}
	}
}

// The overlay's invariants: dependencies point backwards (a DAG), no
// association pair is written twice, redirect chains stay inside the
// resolver's hop bound, and the dense profile lands near the real-brain
// averages it is calibrated to.
func TestGenerateCorpus_Invariants(t *testing.T) {
	c, err := GenerateCorpus(testParams(10000), ProfileDense, 1000)
	if err != nil {
		t.Fatal(err)
	}
	pairs := map[[3]string]bool{}
	for _, b := range c.Batches {
		for _, d := range b.Dependencies {
			if d.DependsOn >= d.EventID {
				t.Fatalf("dependency %s -> %s does not point at an earlier episode", d.EventID, d.DependsOn)
			}
			if err := d.Validate(); err != nil {
				t.Fatalf("invalid dependency: %v", err)
			}
		}
		for _, r := range b.Relationships {
			k := [3]string{string(r.Type), r.FromClaimID, r.ToClaimID}
			if pairs[k] {
				t.Fatalf("duplicate association %v", k)
			}
			pairs[k] = true
		}
	}
	s := c.Stats
	if s.MaxRedirectChain < 1 || s.MaxRedirectChain >= maxRedirectHops {
		t.Fatalf("max redirect chain %d, want 1..%d", s.MaxRedirectChain, maxRedirectHops-1)
	}
	if s.Dependencies == 0 || s.Redirects == 0 {
		t.Fatalf("overlay produced no dependencies (%d) or redirects (%d)", s.Dependencies, s.Redirects)
	}
	// Real brain: ~9.6 association endpoints and ~3.6 evidence links per belief.
	if s.MeanDegree < 8 || s.MeanDegree > 11.5 {
		t.Fatalf("dense mean degree %.2f, want ~9.6", s.MeanDegree)
	}
	if s.EvidencePerBelief < 3 || s.EvidencePerBelief > 4.2 {
		t.Fatalf("dense evidence per belief %.2f, want ~3.6", s.EvidencePerBelief)
	}
}

func TestSummarize(t *testing.T) {
	var ds []time.Duration
	for i := 1; i <= 100; i++ {
		ds = append(ds, time.Duration(i)*time.Millisecond)
	}
	s := summarize(ds)
	if s.Samples != 100 || s.P50MS != 50 || s.P95MS != 95 || s.P99MS != 99 || s.MaxMS != 100 {
		t.Fatalf("summarize = %+v", s)
	}
	if got := summarize(nil); got.Samples != 0 {
		t.Fatalf("empty summarize = %+v", got)
	}
}

func TestCanonicalIgnoresOrder(t *testing.T) {
	a := canonical([]string{"b", "a", "c"})
	b := canonical([]string{"c", "b", "a"})
	c := canonical([]string{"c", "b"})
	if a != b || a == c {
		t.Fatalf("canonical: %s %s %s", a, b, c)
	}
}

func TestParseLoadAvg(t *testing.T) {
	for in, want := range map[string]float64{
		"{ 5.83 8.60 12.72 }\n":       5.83,
		"0.52 0.40 0.31 1/123 4567\n": 0.52,
		"garbage":                     -1,
	} {
		if got := parseLoadAvg(in); got != want {
			t.Errorf("parseLoadAvg(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseDockerBytes(t *testing.T) {
	for in, want := range map[string]int64{"1.5GiB": 1610612736, "512MiB": 536870912, "980kB": 980000, "x": 0} {
		if got := parseDockerBytes(in); got != want {
			t.Errorf("parseDockerBytes(%q) = %d, want %d", in, got, want)
		}
	}
}

// The oracle on a hand-built graph, so it is checked against answers worked
// out by hand rather than against another implementation.
func TestMemGraph_HandBuilt(t *testing.T) {
	at := scalebench.DefaultAnchor
	cl := func(id string, st domain.ClaimStatus, merged string) domain.Claim {
		return domain.Claim{ID: id, Status: st, MergedInto: merged}
	}
	rel := func(id, typ, from, to string, dt time.Duration) domain.Relationship {
		return domain.Relationship{ID: id, Type: domain.RelationshipType(typ), FromClaimID: from, ToClaimID: to, CreatedAt: at.Add(dt)}
	}
	b := Batch{}
	b.Claims = []domain.Claim{
		cl("a", domain.ClaimStatusActive, ""), cl("b", domain.ClaimStatusActive, ""), cl("c", domain.ClaimStatusActive, ""),
		cl("d", domain.ClaimStatusDeprecated, ""), cl("r1", domain.ClaimStatusDeprecated, "r2"),
		cl("r2", domain.ClaimStatusDeprecated, "c"), cl("r3", domain.ClaimStatusDeprecated, "d"),
	}
	b.Relationships = []domain.Relationship{
		rel("ab", "supports", "a", "b", 1), rel("bc", "contradicts", "b", "c", 2), rel("cd", "contradicts", "d", "c", 3),
	}
	// e2 copies e1, e3 copies e2. Belief a rests on e1 only, b on e1 and e4,
	// c on e3 only, d on e1 only (already deprecated).
	b.Evidence = []domain.ClaimEvidence{{ClaimID: "a", EventID: "e1"}, {ClaimID: "b", EventID: "e1"}, {ClaimID: "b", EventID: "e4"},
		{ClaimID: "c", EventID: "e3"}, {ClaimID: "d", EventID: "e1"}}
	b.Dependencies = []domain.EventDependency{{EventID: "e2", DependsOn: "e1"}, {EventID: "e3", DependsOn: "e2"}}
	g := &memGraph{claims: map[string]memClaim{}, adj: map[string][]memEdge{}, evByClaim: map[string][]string{}, claimsByEv: map[string][]string{},
		depsOut: map[string][]string{}, depsIn: map[string][]string{}, contradictBy: map[string][]string{}}
	g.add(b)
	g.sortContradictions()
	ctx := context.Background()
	check := func(name string, got []string, err error, want ...string) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	got, err := g.expand(ctx, ExpandInput{Seeds: []string{"a"}, Hops: 2})
	check("expand", got, err, expandLine("b", 1), expandLine("c", 2))
	got, err = g.retract(ctx, RetractInput{Root: "e1"})
	check("retract", got, err, closureLine("e2", 1), closureLine("e3", 2),
		outcomeLine("a", "retracted"), outcomeLine("b", "rescored"), outcomeLine("c", "retracted"), outcomeLine("d", "already-deprecated"))
	got, err = g.dissonancePage(ctx, PageInput{Limit: 1, Offset: 0})
	check("dissonance", got, err, totalLine(2), relLine("cd"))
	got, err = g.neighbourhood(ctx, IDsInput{IDs: []string{"b"}})
	check("neighbourhood", got, err, relLine("bc"))
	got, err = g.redirects(ctx, IDsInput{IDs: []string{"r1", "r3", "a", "r1"}})
	check("redirects", got, err, redirectLine("r1", "c"))
	got, err = g.provenance(ctx, IDsInput{IDs: []string{"c"}})
	check("provenance", got, err, episodeLine("e3", 0), episodeLine("e2", 1), episodeLine("e1", 2))
}

// Every relational path — production and single-query — returns exactly the
// oracle's answer on a real SQLite store loaded with a generated corpus. This
// is the parity check the benchmark runs, in miniature and without Docker.
func TestSQLitePathsMatchOracle(t *testing.T) {
	ctx := context.Background()
	for _, profile := range []Profile{ProfileUniform, ProfileDense} {
		t.Run(string(profile), func(t *testing.T) {
			c, err := GenerateCorpus(testParams(1000), profile, 500)
			if err != nil {
				t.Fatal(err)
			}
			mem := newMemGraph(c)
			mem.sortContradictions()
			s, err := openRel(ctx, "sqlite", "sqlite://"+filepath.Join(t.TempDir(), "gb.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			if err := s.load(ctx, c); err != nil {
				t.Fatal(err)
			}
			in := sampleInputs(c, 2, 20)
			oracles := map[string]func(context.Context, any) ([]string, error){
				wExpand2: typed(mem.expand), wExpand5: typed(mem.expand), wRetract: typed(mem.retract),
				wDissonance: typed(mem.dissonancePage), wNeighbour: typed(mem.neighbourhood),
				wRedirects: typed(mem.redirects), wProvenance: typed(mem.provenance),
			}
			inputs := map[string][]any{
				wExpand2: asAny(in.Expand2[:10]), wExpand5: asAny(in.Expand5[:5]), wRetract: asAny(in.Retract),
				wDissonance: asAny(in.Pages), wNeighbour: asAny(in.Neighbour), wRedirects: asAny(in.Redirects),
				wProvenance: asAny(in.Provenance),
			}
			nonEmpty := map[string]int{}
			for w, vs := range s.variants() {
				if w == wWrite {
					continue
				}
				for _, v := range vs {
					for i, x := range inputs[w] {
						want, err := oracles[w](ctx, x)
						if err != nil {
							t.Fatal(err)
						}
						got, err := v.run(ctx, x)
						if err != nil {
							t.Fatalf("%s %s: %v", w, v.Path, err)
						}
						wh := canonical(want)
						gh := canonical(got)
						if wh != gh {
							slices.Sort(got)
							slices.Sort(want)
							t.Fatalf("%s %s input %d:\n got %q\nwant %q", w, v.Path, i, head(got), head(want))
						}
						if len(want) > 0 {
							nonEmpty[w]++
						}
					}
				}
			}
			// A parity check over empty answers proves nothing.
			for w := range oracles {
				if nonEmpty[w] == 0 {
					t.Errorf("%s: every answer was empty", w)
				}
			}
			for _, v := range s.variants()[wWrite] {
				if _, err := v.run(ctx, in.Writes[map[string]int{"production": 0, "ports-only": 1}[v.Path]]); err != nil {
					t.Fatalf("write %s: %v", v.Path, err)
				}
			}
		})
	}
}

func head(xs []string) []string { return xs[:min(len(xs), 8)] }
