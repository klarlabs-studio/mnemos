package main

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/scalebench"
)

// Profile selects how the scalebench corpus is overlaid.
//
// The scalebench generator (internal/scalebench) has no episode dependencies
// (ADR 0038) and no merge redirects (ADR 0033), and its association density is
// far below a real brain's. Every profile adds dependencies and redirects so
// workloads 2, 4 and 5 have something to traverse; the dense profile also adds
// associations and evidence until the averages match the shape statistics of a
// real brain (see the README).
type Profile string

const (
	// ProfileUniform is the scalebench uniform corpus unchanged, plus
	// dependencies and redirects.
	ProfileUniform Profile = "uniform"
	// ProfileDense additionally calibrates association and evidence density
	// to a real brain: ~4.8 associations and ~3.6 evidence links per belief.
	ProfileDense Profile = "dense"
)

// Overlay parameters. They are constants rather than flags so a profile name
// fully determines the corpus.
const (
	// depEpisodeP is the share of episodes that depend on earlier ones.
	// Declared dependence (copies, citations) is sparse by design.
	depEpisodeP = 0.08
	// depHubP is the share of dependencies that point at one of the first
	// depHubs episodes: widely-copied sources whose retraction reaches far.
	depHubP = 0.3
	depHubs = 20
	// depRecentWindow bounds how far back a non-hub dependency points, so
	// copies of copies form chains.
	depRecentWindow = 20
	// mergeChainShare is chains per belief; each chain is 1–7 redirects
	// long, so roughly 1.6% of beliefs are redirects.
	mergeChainShare = 0.01
	// denseExtraMean is the mean number of extra supports edges per belief
	// in the dense profile (scalebench adds ~0.8 of its own).
	denseExtraMean = 4.0
	// denseCrossTopicP is the share of extra edges that leave the topic.
	denseCrossTopicP = 0.1
	// denseIsolatedP is the share of beliefs that get no extra edges; a real
	// brain has ~21% beliefs with no association at all.
	denseIsolatedP = 0.2
	// denseExtraEvidenceMean is the mean number of extra evidence links per
	// belief in the dense profile (scalebench adds ~0.45 of its own).
	denseExtraEvidenceMean = 2.1
	// beliefsPerEpisode mirrors scalebench's beliefsPerEvent.
	beliefsPerEpisode = 5
)

// Batch is a scalebench batch plus the overlay's dependencies.
type Batch struct {
	scalebench.Batch
	Dependencies []domain.EventDependency
}

// Corpus is a fully materialised corpus. graphbench loads the same batches
// into every store, so holding them once is what guarantees identical data.
type Corpus struct {
	Params  scalebench.Params
	Profile Profile
	Batches []Batch
	Stats   CorpusStats
}

// CorpusStats summarises a corpus, including the shape statistics compared
// against a real brain.
type CorpusStats struct {
	Beliefs       int            `json:"beliefs"`
	Episodes      int            `json:"episodes"`
	Evidence      int            `json:"evidence"`
	Relationships map[string]int `json:"relationships"`
	Dependencies  int            `json:"dependencies"`
	Redirects     int            `json:"redirects"`
	Deprecated    int            `json:"deprecated"`
	// MeanDegree is association endpoints per belief.
	MeanDegree float64 `json:"mean_degree"`
	MaxDegree  int     `json:"max_degree"`
	// DegreeHistogram uses the buckets 0, 1, 2-3, 4-10, 11-100, >100.
	DegreeHistogram    map[string]int `json:"degree_histogram"`
	EvidencePerBelief  float64        `json:"evidence_per_belief"`
	BeliefsPerEpisode  float64        `json:"beliefs_per_episode"`
	MaxRedirectChain   int            `json:"max_redirect_chain"`
	EpisodesWithDepend int            `json:"episodes_with_dependencies"`
}

// GenerateCorpus builds the corpus for p and profile. It is a pure function
// of its arguments.
func GenerateCorpus(p scalebench.Params, profile Profile, batchSize int) (*Corpus, error) {
	if profile != ProfileUniform && profile != ProfileDense {
		return nil, fmt.Errorf("graphbench: unknown profile %q", profile)
	}
	g, err := scalebench.NewGenerator(p)
	if err != nil {
		return nil, err
	}
	// The overlay draws from its own stream, so the scalebench part of the
	// corpus is byte-identical to what scalebench itself loads.
	rng := rand.New(rand.NewPCG(p.Seed^0x67726170, p.Seed+0x68626e63))
	ov := &overlay{p: p, profile: profile, rng: rng, pairs: map[[2]int32]struct{}{}}
	ov.planMerges()

	c := &Corpus{Params: p, Profile: profile}
	for !g.Done() {
		b := g.Next(batchSize)
		c.Batches = append(c.Batches, ov.extend(b))
	}
	c.Stats = computeStats(c)
	return c, nil
}

type overlay struct {
	p       scalebench.Params
	profile Profile
	rng     *rand.Rand
	// pairs holds every association endpoint pair (both directions), so the
	// overlay never writes an edge the unique (type, from, to) index rejects
	// and never doubles an existing edge.
	pairs map[[2]int32]struct{}
	// redirect maps a belief index to the belief it is merged into.
	redirect map[int]int
	nTopics  int
}

// planMerges lays out the merge chains up front. A chain b0 -> b1 -> ... -> bL
// makes every b_i (i < L) a deprecated redirect to b_{i+1}; bL is the winner
// as generated (and resolves to nothing when the generator deprecated it).
func (o *overlay) planMerges() {
	o.redirect = map[int]int{}
	o.nTopics = max(1, (o.p.Beliefs+199)/200)
	chains := int(float64(o.p.Beliefs) * mergeChainShare)
	used := map[int]bool{}
	for range chains {
		var l int
		switch r := o.rng.Float64(); {
		case r < 0.6:
			l = 1
		case r < 0.85:
			l = 2
		case r < 0.95:
			l = 3
		default:
			l = 4 + o.rng.IntN(4) // 4..7, inside maxRedirectHops = 8
		}
		if l+1 > o.p.Beliefs {
			continue
		}
		nodes := make([]int, 0, l+1)
		for len(nodes) < l+1 {
			i := o.rng.IntN(o.p.Beliefs)
			if !used[i] {
				used[i] = true
				nodes = append(nodes, i)
			}
		}
		for k := 0; k < l; k++ {
			o.redirect[nodes[k]] = nodes[k+1]
		}
	}
}

func claimID(i int) string   { return fmt.Sprintf("sb-cl-%09d", i) }
func episodeID(e int) string { return fmt.Sprintf("sb-ev-%09d", e) }

// claimIndex parses an "sb-cl-%09d" id.
func claimIndex(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "sb-cl-"))
	if err != nil {
		panic(fmt.Sprintf("graphbench: not a corpus belief id: %q", id))
	}
	return n
}

func (o *overlay) addPair(a, b int) bool {
	k := [2]int32{int32(a), int32(b)}
	if _, dup := o.pairs[k]; dup {
		return false
	}
	o.pairs[k] = struct{}{}
	o.pairs[[2]int32{int32(b), int32(a)}] = struct{}{}
	return true
}

func (o *overlay) extend(sb scalebench.Batch) Batch {
	out := Batch{Batch: sb}
	for _, r := range sb.Relationships {
		o.addPair(claimIndex(r.FromClaimID), claimIndex(r.ToClaimID))
	}
	// Dependencies, emitted with the episode that declares them.
	for _, ev := range sb.Events {
		e, err := strconv.Atoi(strings.TrimPrefix(ev.ID, "sb-ev-"))
		if err != nil {
			panic(err)
		}
		out.Dependencies = append(out.Dependencies, o.dependencies(e, ev)...)
	}
	// Merge redirects: deprecated, pointing at the next link of the chain.
	for k := range out.Claims {
		i := claimIndex(out.Claims[k].ID)
		if to, ok := o.redirect[i]; ok {
			out.Claims[k].Status = domain.ClaimStatusDeprecated
			out.Claims[k].MergedInto = claimID(to)
		}
	}
	if o.profile == ProfileDense {
		for _, c := range sb.Claims {
			i := claimIndex(c.ID)
			out.Relationships = append(out.Relationships, o.denseEdges(i, c)...)
			out.Evidence = append(out.Evidence, o.denseEvidence(i, sb.Evidence)...)
		}
	}
	return out
}

func (o *overlay) dependencies(e int, ev domain.Event) []domain.EventDependency {
	if e == 0 || o.rng.Float64() >= depEpisodeP {
		return nil
	}
	n := 1
	if o.rng.Float64() < 0.2 {
		n = 2
	}
	seen := map[int]bool{}
	var out []domain.EventDependency
	for range n {
		var t int
		if o.rng.Float64() < depHubP {
			t = o.rng.IntN(min(depHubs, e))
		} else {
			t = e - 1 - o.rng.IntN(min(depRecentWindow, e))
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, domain.EventDependency{
			EventID: ev.ID, DependsOn: episodeID(t),
			Kind: domain.DependencyCopies, Signal: domain.SignalMetadata,
			Reference: "graphbench overlay", CreatedAt: ev.IngestedAt, CreatedBy: "<system>",
		})
	}
	return out
}

// poisson draws from a Poisson distribution with the given mean (Knuth).
func poisson(rng *rand.Rand, mean float64) int {
	l, k, p := math.Exp(-mean), 0, 1.0
	for {
		p *= rng.Float64()
		if p <= l {
			return k
		}
		k++
	}
}

// denseEdges adds supports edges from belief i to earlier beliefs: mostly in
// its own topic with preferential attachment towards the topic's oldest
// beliefs (which become hubs), some anywhere in the brain.
func (o *overlay) denseEdges(i int, c domain.Claim) []domain.Relationship {
	if i == 0 || o.rng.Float64() < denseIsolatedP {
		return nil
	}
	k := poisson(o.rng, denseExtraMean/(1-denseIsolatedP))
	topic := i % o.nTopics
	earlier := (i - topic) / o.nTopics
	var out []domain.Relationship
	for range k {
		var t int
		if earlier == 0 || o.rng.Float64() < denseCrossTopicP {
			t = o.rng.IntN(i)
		} else {
			u := o.rng.Float64()
			t = topic + o.nTopics*int(float64(earlier)*u*u)
		}
		if t == i || !o.addPair(i, t) {
			continue
		}
		out = append(out, domain.Relationship{
			ID:          fmt.Sprintf("gb-rel-%09d-%09d", i, t),
			Type:        domain.RelationshipTypeSupports,
			FromClaimID: c.ID,
			ToClaimID:   claimID(t),
			CreatedAt:   c.CreatedAt,
			CreatedBy:   "<system>",
		})
	}
	return out
}

// denseEvidence adds evidence links from belief i to recent earlier episodes.
func (o *overlay) denseEvidence(i int, existing []domain.ClaimEvidence) []domain.ClaimEvidence {
	own := i / beliefsPerEpisode
	if own == 0 {
		return nil
	}
	id := claimID(i)
	have := map[string]bool{}
	for _, l := range existing {
		if l.ClaimID == id {
			have[l.EventID] = true
		}
	}
	var out []domain.ClaimEvidence
	for range poisson(o.rng, denseExtraEvidenceMean) {
		e := episodeID(own - 1 - o.rng.IntN(min(50, own)))
		if have[e] {
			continue
		}
		have[e] = true
		out = append(out, domain.ClaimEvidence{ClaimID: id, EventID: e})
	}
	return out
}

func computeStats(c *Corpus) CorpusStats {
	s := CorpusStats{Relationships: map[string]int{}, DegreeHistogram: map[string]int{}}
	degree := map[string]int{}
	merged := map[string]string{}
	depEpisodes := map[string]bool{}
	for _, b := range c.Batches {
		s.Beliefs += len(b.Claims)
		s.Episodes += len(b.Events)
		s.Evidence += len(b.Evidence)
		s.Dependencies += len(b.Dependencies)
		for _, d := range b.Dependencies {
			depEpisodes[d.EventID] = true
		}
		for _, cl := range b.Claims {
			if cl.Status == domain.ClaimStatusDeprecated {
				s.Deprecated++
			}
			if cl.MergedInto != "" {
				s.Redirects++
				merged[cl.ID] = cl.MergedInto
			}
		}
		for _, r := range b.Relationships {
			s.Relationships[string(r.Type)]++
			degree[r.FromClaimID]++
			degree[r.ToClaimID]++
		}
	}
	s.EpisodesWithDepend = len(depEpisodes)
	total := 0
	for i := 0; i < s.Beliefs; i++ {
		d := degree[claimID(i)]
		total += d
		s.MaxDegree = max(s.MaxDegree, d)
		s.DegreeHistogram[degreeBucket(d)]++
	}
	if s.Beliefs > 0 {
		s.MeanDegree = round3(float64(total) / float64(s.Beliefs))
		s.EvidencePerBelief = round3(float64(s.Evidence) / float64(s.Beliefs))
	}
	if s.Episodes > 0 {
		s.BeliefsPerEpisode = round3(float64(s.Evidence) / float64(s.Episodes))
	}
	for id := range merged {
		n := 0
		for cur := id; merged[cur] != "" && n < 16; cur = merged[cur] {
			n++
		}
		s.MaxRedirectChain = max(s.MaxRedirectChain, n)
	}
	return s
}

func degreeBucket(d int) string {
	switch {
	case d == 0:
		return "0"
	case d == 1:
		return "1"
	case d <= 3:
		return "2-3"
	case d <= 10:
		return "4-10"
	case d <= 100:
		return "11-100"
	default:
		return ">100"
	}
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
