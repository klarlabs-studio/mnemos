package scalebench

import (
	"crypto/sha256"
	"encoding/json"
	"hash"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

// digest hashes each row stream separately, so two batch sizes that produce
// the same rows in the same order hash identically even though they group them
// differently.
func digest(t *testing.T, p Params, batch int) ([32]byte, Stats) {
	t.Helper()
	g, err := NewGenerator(p)
	if err != nil {
		t.Fatal(err)
	}
	streams := [4]hash.Hash{sha256.New(), sha256.New(), sha256.New(), sha256.New()}
	enc := func(i int, v any) {
		if err := json.NewEncoder(streams[i]).Encode(v); err != nil {
			t.Fatal(err)
		}
	}
	for !g.Done() {
		b := g.Next(batch)
		for _, v := range b.Events {
			enc(0, v)
		}
		for _, v := range b.Claims {
			enc(1, v)
		}
		for _, v := range b.Evidence {
			enc(2, v)
		}
		for _, v := range b.Relationships {
			enc(3, v)
		}
	}
	all := sha256.New()
	for _, s := range streams {
		all.Write(s.Sum(nil))
	}
	var sum [32]byte
	copy(sum[:], all.Sum(nil))
	return sum, g.Stats()
}

func params(shape Shape) Params {
	return Params{Beliefs: 3000, Seed: 7, Shape: shape, Anchor: DefaultAnchor, RunID: "scalebench"}
}

// A baseline is only comparable across commits if the corpus is a pure
// function of its params. Batch size must not matter either: it is a loading
// knob, not part of the corpus.
func TestCorpusIsDeterministicAndBatchIndependent(t *testing.T) {
	for _, s := range Shapes {
		a, _ := digest(t, params(s), 500)
		b, _ := digest(t, params(s), 500)
		if a != b {
			t.Fatalf("%s: same params produced different corpora", s)
		}
	}
	// Next draws from the RNG per belief, never per batch, so the batch size
	// must not change a single row.
	a, sa := digest(t, params(ShapeUniform), 1000)
	b, sb := digest(t, params(ShapeUniform), 7)
	if a != b || sa != sb {
		t.Fatalf("batch size changed the corpus: %+v vs %+v", sa, sb)
	}
	other := params(ShapeUniform)
	other.Seed = 8
	if c, _ := digest(t, other, 1000); c == a {
		t.Fatal("different seeds produced identical corpora")
	}
}

func TestCorpusIsValidAndReferentiallyClosed(t *testing.T) {
	for _, s := range Shapes {
		g, err := NewGenerator(params(s))
		if err != nil {
			t.Fatal(err)
		}
		events, claims := map[string]bool{}, map[string]bool{}
		for !g.Done() {
			b := g.Next(250)
			for _, e := range b.Events {
				if err := e.Validate(); err != nil {
					t.Fatalf("%s: invalid episode: %v", s, err)
				}
				events[e.ID] = true
			}
			for _, c := range b.Claims {
				if err := c.Validate(); err != nil {
					t.Fatalf("%s: invalid belief: %v", s, err)
				}
				claims[c.ID] = true
			}
			for _, ev := range b.Evidence {
				if !events[ev.EventID] || !claims[ev.ClaimID] {
					t.Fatalf("%s: evidence %+v references a row not yet generated", s, ev)
				}
			}
			for _, r := range b.Relationships {
				if err := r.Validate(); err != nil {
					t.Fatalf("%s: invalid association: %v", s, err)
				}
				if !claims[r.FromClaimID] || !claims[r.ToClaimID] {
					t.Fatalf("%s: association %s references a belief not yet generated", s, r.ID)
				}
			}
		}
		if got := g.Stats().Claims; got != 3000 {
			t.Fatalf("%s: generated %d beliefs, want 3000", s, got)
		}
	}
}

// The pathological shapes exist to stress specific operations; if a shape
// stops producing its pathology the gate built on it measures nothing.
func TestShapesProduceTheirPathology(t *testing.T) {
	stats := func(s Shape) (Stats, map[string]int, int) {
		g, _ := NewGenerator(params(s))
		inDeg := map[string]int{}
		maxEv := 0
		for !g.Done() {
			b := g.Next(1000)
			perClaim := map[string]int{}
			for _, ev := range b.Evidence {
				perClaim[ev.ClaimID]++
			}
			for _, n := range perClaim {
				maxEv = max(maxEv, n)
			}
			for _, r := range b.Relationships {
				inDeg[r.ToClaimID]++
			}
		}
		return g.Stats(), inDeg, maxEv
	}
	maxDeg := func(m map[string]int) int {
		x := 0
		for _, v := range m {
			x = max(x, v)
		}
		return x
	}
	u, uDeg, uEv := stats(ShapeUniform)
	_, hDeg, _ := stats(ShapeHub)
	if maxDeg(hDeg) < 10*maxDeg(uDeg) {
		t.Errorf("hub: max in-degree %d is not >> uniform %d", maxDeg(hDeg), maxDeg(uDeg))
	}
	c, _, _ := stats(ShapeContradiction)
	if float64(c.Contradicts)/float64(c.Relationships) < 0.5 || float64(u.Contradicts)/float64(u.Relationships) > 0.3 {
		t.Errorf("contradiction share: shape %d/%d, uniform %d/%d", c.Contradicts, c.Relationships, u.Contradicts, u.Relationships)
	}
	_, _, sEv := stats(ShapeSkewedEvidence)
	if sEv < 5*uEv {
		t.Errorf("skewed-evidence: max links per belief %d is not >> uniform %d", sEv, uEv)
	}

	stale := 0
	g, _ := NewGenerator(params(ShapeStale))
	for !g.Done() {
		for _, cl := range g.Next(1000).Claims {
			if DefaultAnchor.Sub(cl.CreatedAt).Hours() > 730*24 {
				stale++
			}
		}
	}
	if stale < 2000 {
		t.Errorf("stale: only %d of 3000 beliefs are older than two years", stale)
	}
	gc, _ := NewGenerator(params(ShapeCommonToken))
	if len(gc.vocab) > 50 {
		t.Errorf("common-token vocabulary has %d words", len(gc.vocab))
	}
	_ = domain.ClaimTypeFact
}
