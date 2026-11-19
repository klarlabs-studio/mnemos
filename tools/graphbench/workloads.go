package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Workload inputs. Each is a value, so the same slice of inputs is replayed
// against every store in the same order.
type (
	// ExpandInput is a recall's seed beliefs and hop budget.
	ExpandInput struct {
		Seeds []string
		Hops  int
	}
	// RetractInput names the episode a retraction starts from.
	RetractInput struct{ Root string }
	// PageInput is one list_dissonances page.
	PageInput struct{ Limit, Offset int }
	// IDsInput is a set of belief ids.
	IDsInput struct{ IDs []string }
	// WriteInput is one batch of new beliefs, episodes, evidence, edges and
	// dependencies.
	WriteInput struct{ Batch Batch }
)

// Workload names, in report order.
const (
	wExpand2      = "recall_expand_hops2"
	wExpand5      = "recall_expand_hops5"
	wRetract      = "retraction_closure"
	wDissonance   = "dissonance_page"
	wNeighbour    = "contradiction_neighbourhood"
	wRedirects    = "merge_redirects"
	wProvenance   = "provenance_path"
	wWrite        = "write_batch"
	inputsPerLoad = 50
)

// Inputs holds every workload's deterministic input list.
type Inputs struct {
	Expand2, Expand5 []ExpandInput
	Retract          []RetractInput
	Pages            []PageInput
	Neighbour        []IDsInput
	Redirects        []IDsInput
	Provenance       []IDsInput
	Writes           []WriteInput
}

// sampleInputs draws the inputs from the corpus with their own seed, so they
// are a pure function of the corpus params.
func sampleInputs(c *Corpus, writes, writeBeliefs int) Inputs {
	rng := rand.New(rand.NewPCG(c.Params.Seed^0x696e70, 0x75747321))
	n := c.Stats.Beliefs
	episodes := c.Stats.Episodes
	nTopics := max(1, (n+199)/200)
	var redirects []string
	for _, b := range c.Batches {
		for _, cl := range b.Claims {
			if cl.MergedInto != "" {
				redirects = append(redirects, cl.ID)
			}
		}
	}
	randomIDs := func(k int) []string {
		out := make([]string, k)
		for i := range out {
			out[i] = claimID(rng.IntN(n))
		}
		return out
	}
	var in Inputs
	for range inputsPerLoad {
		// A recall's seeds are the beliefs one query matched, which share
		// topic words: ten beliefs from one topic.
		topic := rng.IntN(nTopics)
		perTopic := max(1, (n-topic+nTopics-1)/nTopics)
		seeds := make([]string, 0, 10)
		seen := map[int]bool{}
		for len(seeds) < min(10, perTopic) {
			i := topic + nTopics*rng.IntN(perTopic)
			if i < n && !seen[i] {
				seen[i] = true
				seeds = append(seeds, claimID(i))
			}
		}
		in.Expand2 = append(in.Expand2, ExpandInput{Seeds: seeds, Hops: 2})
		in.Expand5 = append(in.Expand5, ExpandInput{Seeds: seeds, Hops: 5})

		// Half the retractions hit a widely-copied source, half a random
		// episode (usually nothing depends on it, the common case).
		var root int
		if len(in.Retract)%2 == 0 {
			root = rng.IntN(min(depHubs, episodes))
		} else {
			root = rng.IntN(episodes)
		}
		in.Retract = append(in.Retract, RetractInput{Root: episodeID(root)})

		in.Neighbour = append(in.Neighbour, IDsInput{IDs: randomIDs(50)})
		ids := randomIDs(10)
		for range 10 {
			if len(redirects) > 0 {
				ids = append(ids, redirects[rng.IntN(len(redirects))])
			}
		}
		in.Redirects = append(in.Redirects, IDsInput{IDs: ids})
		in.Provenance = append(in.Provenance, IDsInput{IDs: randomIDs(20)})
	}
	contradicts := c.Stats.Relationships[string(domain.RelationshipTypeContradicts)]
	for off := 0; off < 10*50 && off < max(contradicts, 1); off += 50 {
		in.Pages = append(in.Pages, PageInput{Limit: 50, Offset: off})
	}
	for w := range writes {
		in.Writes = append(in.Writes, writeBatch(rng, c, w, writeBeliefs))
	}
	return in
}

// writeBatch builds one ingest-sized batch: writeBeliefs new beliefs over
// writeBeliefs/5 new episodes, each belief with its episode's evidence plus,
// in the dense profile, links to recent episodes, and associations to
// existing beliefs at the corpus's density.
func writeBatch(rng *rand.Rand, c *Corpus, w, beliefs int) WriteInput {
	n, episodes := c.Stats.Beliefs, c.Stats.Episodes
	at := c.Params.Anchor.Add(time.Duration(w+1) * time.Minute)
	var b Batch
	nEp := max(1, beliefs/beliefsPerEpisode)
	for e := range nEp {
		id := fmt.Sprintf("gbw-ev-%04d-%03d", w, e)
		b.Events = append(b.Events, domain.Event{
			ID: id, RunID: c.Params.RunID, SchemaVersion: "v1",
			Content:       fmt.Sprintf("graphbench write %d episode %d", w, e),
			SourceInputID: fmt.Sprintf("gbw-src-%d-%d", w, e), Timestamp: at, IngestedAt: at, CreatedBy: "<system>",
		})
		if rng.Float64() < 0.2 {
			b.Dependencies = append(b.Dependencies, domain.EventDependency{
				EventID: id, DependsOn: episodeID(rng.IntN(episodes)), Kind: domain.DependencyCopies,
				Signal: domain.SignalMetadata, Reference: "graphbench write", CreatedAt: at, CreatedBy: "<system>",
			})
		}
	}
	edges := 1
	if c.Profile == ProfileDense {
		edges = 5
	}
	for i := range beliefs {
		id := fmt.Sprintf("gbw-cl-%04d-%04d", w, i)
		b.Claims = append(b.Claims, domain.Claim{
			ID: id, Text: fmt.Sprintf("Graphbench write %d belief %d about service %d", w, i, rng.IntN(1000)),
			Type: domain.ClaimTypeFact, Confidence: 0.8, Status: domain.ClaimStatusActive,
			CreatedAt: at, CreatedBy: "<system>", ValidFrom: at,
		})
		ep := b.Events[i/beliefsPerEpisode%nEp].ID
		b.Evidence = append(b.Evidence, domain.ClaimEvidence{ClaimID: id, EventID: ep})
		if c.Profile == ProfileDense {
			b.Evidence = append(b.Evidence, domain.ClaimEvidence{ClaimID: id, EventID: episodeID(episodes - 1 - rng.IntN(min(50, episodes)))})
		}
		seen := map[int]bool{}
		for range edges {
			t := rng.IntN(n)
			if seen[t] {
				continue
			}
			seen[t] = true
			typ := domain.RelationshipTypeSupports
			if rng.Float64() < 0.05 {
				typ = domain.RelationshipTypeContradicts
			}
			b.Relationships = append(b.Relationships, domain.Relationship{
				ID: fmt.Sprintf("gbw-rel-%04d-%04d-%09d", w, i, t), Type: typ,
				FromClaimID: id, ToClaimID: claimID(t), CreatedAt: at, CreatedBy: "<system>",
			})
		}
	}
	return WriteInput{Batch: b}
}

// Variant is one way of answering a workload on one store.
type Variant struct {
	Store string `json:"store"`
	// Path is "production" (the current code path, or one composed from the
	// production repository reads where the code path is unexported),
	// "single-query" (one recursive CTE / one SQL statement), "cypher",
	// "cypher-per-hop" or "adjacency-map".
	Path string `json:"path"`
	run  func(ctx context.Context, in any) ([]string, error)
}

// typed adapts a typed workload function to a Variant runner.
func typed[T any](fn func(context.Context, T) ([]string, error)) func(context.Context, any) ([]string, error) {
	return func(ctx context.Context, in any) ([]string, error) {
		v, ok := in.(T)
		if !ok {
			return nil, fmt.Errorf("graphbench: input %T is not %T", in, *new(T))
		}
		return fn(ctx, v)
	}
}

func asAny[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}
