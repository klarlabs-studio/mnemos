package brainbench

import (
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Cost side of the Mnemos-vs-baseline comparison.
//
// An accuracy gain only matters "at acceptable cost", so every system the
// battery scores also reports what it spent: time to build its store from the
// corpus, what it stores, and how long each question took. These are wall
// times and byte counts, not metrics. Wall time varies run to run, so putting
// it in a MetricSet would make the noise arm flag every run as
// nondeterministic; and there is no direction to score a cost by without a
// budget, which the harness does not have. The report shows the numbers beside
// the accuracy table and leaves the trade-off to the reader.

// SystemCost is what one answering system spent on one scenario.
type SystemCost struct {
	System string `json:"system"`
	// BuildMS is the time to turn the corpus into the system's store. For
	// Mnemos: the ingest path (extraction, dedupe, relationship detection,
	// persistence, stub embedding) summed over documents. For a baseline:
	// sentence chunking and tokenisation of every document.
	BuildMS float64 `json:"build_ms"`
	// CurationMS is the time spent applying a timeline's structured changes.
	// Mnemos only; a baseline has no curation to apply.
	CurationMS float64 `json:"curation_ms,omitempty"`
	// StoredBytes is the size of what the system keeps. For Mnemos, the
	// SQLite file and sidecars of the untreated arm, read before any probe
	// (schema, indexes, history tables and vectors included). For a baseline,
	// the in-memory index: chunk text plus token bytes; it is never persisted.
	StoredBytes int64 `json:"stored_bytes"`
	// Rows counts the units the system stores, by kind.
	Rows map[string]int `json:"rows"`
	// Queries is how many questions the latency figures cover.
	Queries     int     `json:"queries"`
	QueryMeanMS float64 `json:"query_mean_ms"`
	QueryP50MS  float64 `json:"query_p50_ms"`
	QueryMaxMS  float64 `json:"query_max_ms"`
}

// CostReport is the cost side of one scenario.
type CostReport struct {
	// CorpusBytes is the raw text every system was built from.
	CorpusBytes int64        `json:"corpus_bytes"`
	Systems     []SystemCost `json:"systems"`
}

// SystemMnemos names the Mnemos recall system in the cost report.
const SystemMnemos = "mnemos"

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// withLatency fills the latency fields from per-question wall times.
func (c SystemCost) withLatency(lat []time.Duration) SystemCost {
	c.Queries = len(lat)
	if len(lat) == 0 {
		return c
	}
	sorted := append([]time.Duration(nil), lat...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	c.QueryMeanMS = ms(sum) / float64(len(sorted))
	c.QueryP50MS = ms(sorted[(len(sorted)-1)/2])
	c.QueryMaxMS = ms(sorted[len(sorted)-1])
	return c
}

// mnemosCost reads the untreated arm's census and the seed's timings.
func mnemosCost(seed SeedStats, control MetricSet) SystemCost {
	count := func(name string) int {
		if m, ok := control.Get(name); ok && m.Known {
			return int(m.Value)
		}
		return 0
	}
	c := SystemCost{
		System:     SystemMnemos,
		BuildMS:    ms(seed.IngestDuration),
		CurationMS: ms(seed.CurationDuration),
		Rows: map[string]int{
			"events":        count(MetricEvents),
			"claims":        count(MetricValidClaims) + count(MetricInvalidatedClaims),
			"evidence":      count(MetricEvidenceLinks),
			"relationships": count(MetricRelationships),
			"embeddings":    seed.Embeddings,
		},
	}
	if m, ok := control.Get(MetricDBBytes); ok && m.Known {
		c.StoredBytes = int64(m.Value)
	}
	return c.withLatency(control.latencies)
}

// baselineCost describes a raw retriever's index.
func baselineCost(name string, r rawRetriever, build time.Duration, lat []time.Duration) SystemCost {
	var bytes int64
	for _, ch := range r.chunks {
		bytes += int64(len(ch.text))
		for _, t := range ch.tokens {
			bytes += int64(len(t))
		}
	}
	return SystemCost{
		System:      name,
		BuildMS:     ms(build),
		StoredBytes: bytes,
		Rows:        map[string]int{"documents": len(r.docs), "chunks": len(r.chunks)},
	}.withLatency(lat)
}

func corpusBytes(events []domain.Event) int64 {
	var n int64
	for _, ev := range events {
		n += int64(len(ev.Content))
	}
	return n
}
