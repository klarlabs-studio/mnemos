// Package scalebench generates deterministic synthetic brains at a chosen size
// and measures how Mnemos's online operations behave against them.
//
// It answers a different question from internal/brainbench. brainbench asks
// whether the cognitive processes IMPROVE a brain and seeds a few hundred
// hand-written documents through the real extraction path. scalebench asks what
// an operation COSTS at 10k, 100k or 1M beliefs. Extracting a million beliefs
// through the pipeline would measure the extractor and take hours, so the corpus
// is generated directly as beliefs, episodes, evidence links and associations
// and bulk-loaded through the storage ports. The measured operations then run
// through the public mnemos API, exactly as a consumer calls them.
//
// Every generated value is a pure function of [Params]. The same params
// produce byte-identical corpora on any machine, so a baseline recorded at one
// commit can be regenerated at another and the difference attributed to the
// code, not the data.
package scalebench

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Shape selects the distribution the corpus is drawn from. Uniform is the
// baseline; the others are the pathological shapes the consolidation plan
// requires the scale gates to cover.
type Shape string

const (
	// ShapeUniform spreads beliefs evenly across topics, with modest evidence
	// and association fan-out.
	ShapeUniform Shape = "uniform"
	// ShapeHub concentrates associations on a few high-degree beliefs, the
	// worst case for graph expansion and spreading activation.
	ShapeHub Shape = "hub"
	// ShapeContradiction makes a large share of associations `contradicts`
	// inside dense clusters, the worst case for contradiction discovery.
	ShapeContradiction Shape = "contradiction"
	// ShapeCommonToken draws belief text from a tiny vocabulary, so almost
	// every pair overlaps. This is the worst case for token-overlap recall and
	// relationship detection.
	ShapeCommonToken Shape = "common-token"
	// ShapeSkewedEvidence gives a few beliefs most of the evidence links
	// (Pareto), the worst case for trust and evidence aggregation.
	ShapeSkewedEvidence Shape = "skewed-evidence"
	// ShapeStale ages most beliefs far past their half-life, the worst case for
	// decay, staleness scans and forgetting.
	ShapeStale Shape = "stale"
)

// Shapes lists every supported shape, in a stable order.
var Shapes = []Shape{ShapeUniform, ShapeHub, ShapeContradiction, ShapeCommonToken, ShapeSkewedEvidence, ShapeStale}

// Params fully determines a corpus.
type Params struct {
	Beliefs int
	Seed    uint64
	Shape   Shape
	// Anchor is the instant every age is measured back from. It is part of
	// the params, not time.Now(), so a corpus regenerated tomorrow is the same
	// corpus.
	Anchor time.Time
	// RunID tags every episode; it is also the scope used by run-scoped reads.
	RunID string
}

// DefaultAnchor is a fixed instant so the default corpus never drifts.
var DefaultAnchor = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// Validate rejects params that cannot generate a meaningful corpus.
func (p Params) Validate() error {
	if p.Beliefs < 1 {
		return fmt.Errorf("scalebench: beliefs must be >= 1, got %d", p.Beliefs)
	}
	for _, s := range Shapes {
		if p.Shape == s {
			if p.Anchor.IsZero() {
				return fmt.Errorf("scalebench: anchor is required")
			}
			return nil
		}
	}
	return fmt.Errorf("scalebench: unknown shape %q", p.Shape)
}

// Batch is one slice of a corpus, sized for a single bulk write.
type Batch struct {
	Events        []domain.Event
	Claims        []domain.Claim
	Evidence      []domain.ClaimEvidence
	Relationships []domain.Relationship
}

// Stats summarises a generated corpus.
type Stats struct {
	Events        int `json:"events"`
	Claims        int `json:"claims"`
	Evidence      int `json:"evidence"`
	Relationships int `json:"relationships"`
	Contradicts   int `json:"contradicts"`
	Topics        int `json:"topics"`
}

// beliefsPerTopic sets topic granularity: related beliefs share topic words,
// which is what gives recall and relationship detection realistic overlap.
const beliefsPerTopic = 200

// beliefsPerEvent is how many beliefs one episode supports on average, which
// is roughly what one captured session yields.
const beliefsPerEvent = 5

// Generator yields a corpus batch by batch, so a million-belief corpus never
// has to be held in memory at once.
type Generator struct {
	p      Params
	rng    *rand.Rand
	topics [][]string
	vocab  []string
	next   int // next belief index
	stats  Stats
}

// NewGenerator validates params and prepares the vocabulary.
func NewGenerator(p Params) (*Generator, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	g := &Generator{p: p, rng: rand.New(rand.NewPCG(p.Seed, p.Seed^0x9e3779b97f4a7c15))}
	vocabSize := 4000
	if p.Shape == ShapeCommonToken {
		vocabSize = 24
	}
	g.vocab = make([]string, vocabSize)
	seen := map[string]bool{}
	for i := range g.vocab {
		w := g.word()
		for seen[w] {
			w = g.word()
		}
		seen[w] = true
		g.vocab[i] = w
	}
	nTopics := max(1, (p.Beliefs+beliefsPerTopic-1)/beliefsPerTopic)
	g.topics = make([][]string, nTopics)
	for i := range g.topics {
		words := make([]string, 6)
		for j := range words {
			words[j] = g.vocab[g.rng.IntN(len(g.vocab))]
		}
		g.topics[i] = words
	}
	g.stats.Topics = nTopics
	return g, nil
}

var syllables = []string{"ka", "lo", "mi", "ra", "ten", "vo", "sul", "dex", "pra", "nor", "qui", "zan", "bel", "tor", "fen", "gra"}

func (g *Generator) word() string {
	// 3–4 syllables from 16 gives ~70k distinct words, so drawing a 4,000-word
	// vocabulary without repeats never approaches exhaustion.
	n := 3 + g.rng.IntN(2)
	var b strings.Builder
	for range n {
		b.WriteString(syllables[g.rng.IntN(len(syllables))])
	}
	return b.String()
}

var templates = []string{
	"The %s service uses %s for %s storage",
	"We decided to migrate %s from %s to %s",
	"%s latency increases when %s exceeds %s",
	"The %s team owns the %s pipeline and %s alerts",
	"Deploying %s requires %s approval before %s",
	"%s depends on %s through the %s gateway",
}

// Done reports whether every belief has been generated.
func (g *Generator) Done() bool { return g.next >= g.p.Beliefs }

// Stats returns the totals generated so far.
func (g *Generator) Stats() Stats { return g.stats }

// Next generates up to n beliefs with their episodes, evidence and outgoing
// associations. Associations only point at beliefs already generated, so every
// batch is loadable on its own without forward references.
func (g *Generator) Next(n int) Batch {
	var b Batch
	end := min(g.p.Beliefs, g.next+n)
	for i := g.next; i < end; i++ {
		topic := i % len(g.topics)
		ts := g.p.Anchor.Add(-g.age())
		// Episode e supports beliefs [e*beliefsPerEvent, (e+1)*beliefsPerEvent).
		// It is emitted with its first belief, never at a batch boundary, so a
		// batch that starts mid-episode reuses the one an earlier batch wrote.
		eventID := fmt.Sprintf("sb-ev-%09d", i/beliefsPerEvent)
		if i%beliefsPerEvent == 0 {
			b.Events = append(b.Events, domain.Event{
				ID:            eventID,
				RunID:         g.p.RunID,
				SchemaVersion: "v1",
				Content:       fmt.Sprintf("synthetic session %d on topic %s", i/beliefsPerEvent, strings.Join(g.topics[topic][:2], " ")),
				SourceInputID: fmt.Sprintf("sb-src-%d", i/beliefsPerEvent),
				Timestamp:     ts,
				IngestedAt:    ts,
				CreatedBy:     "<system>",
			})
			g.stats.Events++
		}
		c := g.claim(i, topic, ts)
		b.Claims = append(b.Claims, c)
		g.stats.Claims++

		b.Evidence = append(b.Evidence, domain.ClaimEvidence{ClaimID: c.ID, EventID: eventID})
		for _, evIdx := range g.extraEvidence(i) {
			b.Evidence = append(b.Evidence, domain.ClaimEvidence{ClaimID: c.ID, EventID: fmt.Sprintf("sb-ev-%09d", evIdx)})
		}
		b.Relationships = append(b.Relationships, g.associations(i, topic, ts)...)
	}
	g.stats.Evidence += len(b.Evidence)
	g.stats.Relationships += len(b.Relationships)
	for _, r := range b.Relationships {
		if r.Type == domain.RelationshipTypeContradicts {
			g.stats.Contradicts++
		}
	}
	g.next = end
	return b
}

// age returns how far before the anchor a belief was recorded.
func (g *Generator) age() time.Duration {
	maxDays := 730.0
	if g.p.Shape == ShapeStale && g.rng.Float64() < 0.8 {
		// Most beliefs sit 2–6 years back, far past any half-life.
		return time.Duration((730 + g.rng.Float64()*1460) * float64(24*time.Hour))
	}
	return time.Duration(g.rng.Float64() * maxDays * float64(24*time.Hour))
}

func (g *Generator) claim(i, topic int, ts time.Time) domain.Claim {
	t := g.topics[topic]
	tpl := templates[g.rng.IntN(len(templates))]
	text := fmt.Sprintf(tpl, t[g.rng.IntN(len(t))], g.vocab[g.rng.IntN(len(g.vocab))], t[g.rng.IntN(len(t))])
	// A unique suffix keeps every belief distinct text, so ingest-style
	// exact-text dedupe never collapses the corpus below its nominal size.
	text = fmt.Sprintf("%s (note %d)", text, i)

	typ := domain.ClaimTypeFact
	switch r := g.rng.Float64(); {
	case r < 0.2:
		typ = domain.ClaimTypeDecision
	case r < 0.35:
		typ = domain.ClaimTypeHypothesis
	}
	status := domain.ClaimStatusActive
	switch r := g.rng.Float64(); {
	case r < 0.03:
		status = domain.ClaimStatusDeprecated
	case r < 0.10:
		status = domain.ClaimStatusContested
	}
	var halfLife float64
	classifier := ""
	switch r := g.rng.Float64(); {
	case r < 0.20:
		halfLife, classifier = 14, "volatility/v1"
	case r < 0.30:
		halfLife, classifier = 0, "volatility/v1"
	case r < 0.33:
		halfLife = 365 // human override
	}
	return domain.Claim{
		ID:                 fmt.Sprintf("sb-cl-%09d", i),
		Text:               text,
		Type:               typ,
		Confidence:         math.Round((0.4+0.6*g.rng.Float64())*1000) / 1000,
		Status:             status,
		CreatedAt:          ts,
		CreatedBy:          "<system>",
		ValidFrom:          ts,
		HalfLifeDays:       halfLife,
		HalfLifeClassifier: classifier,
	}
}

// extraEvidence returns indexes of earlier episodes that also support belief i.
func (g *Generator) extraEvidence(i int) []int {
	maxEvent := i / beliefsPerEvent
	if maxEvent == 0 {
		return nil
	}
	var k int
	switch g.p.Shape {
	case ShapeSkewedEvidence:
		// Pareto(α=1.16): the classic 80/20. Most beliefs get none, a few get
		// hundreds. Capped so one belief cannot reference more episodes than exist.
		k = int(math.Floor(1/math.Pow(1-g.rng.Float64(), 1/1.16))) - 1
		k = min(k, 500, maxEvent)
	default:
		if g.rng.Float64() < 0.3 {
			k = 1 + g.rng.IntN(2)
		}
	}
	seen := map[int]bool{i / beliefsPerEvent: true}
	var out []int
	for range k {
		e := g.rng.IntN(maxEvent)
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

// associations returns the outgoing edges of belief i, pointing only at
// earlier beliefs in the same topic (or at a hub).
func (g *Generator) associations(i, topic int, ts time.Time) []domain.Relationship {
	nTopics := len(g.topics)
	// Earlier beliefs in this topic are topic, topic+nTopics, ... < i.
	earlier := (i - topic) / nTopics
	if earlier == 0 {
		return nil
	}
	pick := func() int { return topic + nTopics*g.rng.IntN(earlier) }

	var targets []int
	contradictP := 0.15
	switch g.p.Shape {
	case ShapeHub:
		// Every belief links to one of 10 hubs; hubs collect ~N/10 edges each.
		if hubs := min(10, i); hubs > 0 {
			targets = append(targets, g.rng.IntN(hubs))
		}
		if g.rng.Float64() < 0.3 {
			targets = append(targets, pick())
		}
	case ShapeContradiction:
		contradictP = 0.7
		for range 3 {
			targets = append(targets, pick())
		}
	default:
		if g.rng.Float64() < 0.6 {
			targets = append(targets, pick())
		}
		if g.rng.Float64() < 0.2 {
			targets = append(targets, pick())
		}
	}

	var out []domain.Relationship
	seen := map[int]bool{}
	for _, tgt := range targets {
		if tgt == i || seen[tgt] {
			continue
		}
		seen[tgt] = true
		typ := domain.RelationshipTypeSupports
		if g.rng.Float64() < contradictP {
			typ = domain.RelationshipTypeContradicts
		}
		out = append(out, domain.Relationship{
			ID:          fmt.Sprintf("sb-rel-%09d-%09d", i, tgt),
			Type:        typ,
			FromClaimID: fmt.Sprintf("sb-cl-%09d", i),
			ToClaimID:   fmt.Sprintf("sb-cl-%09d", tgt),
			CreatedAt:   ts,
			CreatedBy:   "<system>",
		})
	}
	return out
}

// QueryTexts returns n deterministic recall queries drawn from the corpus's
// topic words, so they hit real neighbourhoods rather than nothing.
func (g *Generator) QueryTexts(n int) []string {
	rng := rand.New(rand.NewPCG(g.p.Seed+1, 0x51ed270b))
	out := make([]string, n)
	for i := range out {
		t := g.topics[rng.IntN(len(g.topics))]
		out[i] = t[rng.IntN(len(t))] + " " + t[rng.IntN(len(t))] + " " + g.vocab[rng.IntN(len(g.vocab))]
	}
	return out
}

// IngestTexts returns n deterministic documents for the write-path
// measurement, phrased so rule-based extraction yields beliefs that overlap
// existing topics and so exercise incremental relationship detection.
func (g *Generator) IngestTexts(n int) []string {
	rng := rand.New(rand.NewPCG(g.p.Seed+2, 0x2545f491))
	out := make([]string, n)
	for i := range out {
		t := g.topics[rng.IntN(len(g.topics))]
		out[i] = fmt.Sprintf("We decided that the %s service uses %s for %s storage. The %s team owns the %s pipeline.",
			t[0], t[1], t[2], t[3], t[4])
	}
	return out
}
