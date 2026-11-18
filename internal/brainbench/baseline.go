package brainbench

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// Raw-retrieval baseline.
//
// Everything else in this package compares Mnemos with itself. That answers
// "did consolidation help?" and cannot answer "do Mnemos's semantics beat what
// you would get without them?". The baseline answers the second question: plain
// chunk retrieval over the raw event texts — the documents Mnemos ingested, and
// nothing derived from them — scored by runBattery with the identical questions,
// top-k, instants and gold as Mnemos recall.
//
// It is deliberately a competent baseline, not a straw man:
//
//   - Chunks are sentences, the granularity rule-based extraction produces
//     claims at. Whole-document chunks would carry superseded and current
//     statements together and distort hit and forbidden rates in both
//     directions.
//   - Time-travel questions get the filter a timestamped log can apply: a
//     recorded-as-of question sees only documents ingested by T, a valid-as-of
//     question only documents whose facts were observed by T.
//   - It sees no structure: no claim status, no curation (retractions,
//     archives, supersessions, contradiction edges), no embeddings. That is the
//     difference under test.

// Baseline names.
const (
	BaselineBM25        = "raw_bm25"
	BaselineBM25Recency = "raw_bm25_recency"
)

// BM25 parameters: the textbook defaults, not tuned to any scenario.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// rawDoc is one ingested event, as the baseline sees it.
type rawDoc struct {
	id         string
	observedAt time.Time
	ingestedAt time.Time
}

// rawChunk is one sentence of one document.
type rawChunk struct {
	doc    int
	text   string
	tokens []string
}

// rawRetriever ranks sentence chunks of the raw corpus.
type rawRetriever struct {
	docs    []rawDoc
	chunks  []rawChunk
	recency bool
}

// newRawRetriever chunks events into sentences. Events are sorted by id first
// so the chunk order, and with it every tie-break, does not depend on the
// order a backend lists rows in.
func newRawRetriever(events []domain.Event, recency bool) rawRetriever {
	sorted := append([]domain.Event(nil), events...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	r := rawRetriever{recency: recency}
	for i, ev := range sorted {
		r.docs = append(r.docs, rawDoc{id: ev.ID, observedAt: ev.Timestamp, ingestedAt: ev.IngestedAt})
		for _, s := range splitSentences(ev.Content) {
			r.chunks = append(r.chunks, rawChunk{doc: i, text: s, tokens: tokenize(s)})
		}
	}
	return r
}

// visible reports whether doc d exists for question q.
func (r rawRetriever) visible(d int, q question) bool {
	doc := r.docs[d]
	if !q.RecordedAsOf.IsZero() && doc.ingestedAt.After(q.RecordedAsOf) {
		return false
	}
	if !q.AsOf.IsZero() && doc.observedAt.After(q.AsOf) {
		return false
	}
	return true
}

// recencyWeights gives each visible document a weight in [1, 2]: 1 for the
// oldest, 2 for the newest, linear in chronological RANK. Rank rather than age
// keeps the weighting scale-free, so it behaves the same on a corpus spanning
// two years (back-dated scenarios) and one spanning milliseconds (a real-clock
// timeline). Documents are ordered by when they were written (ingestedAt), then
// by when their facts were observed, then by id.
func (r rawRetriever) recencyWeights(vis []int) map[int]float64 {
	order := append([]int(nil), vis...)
	sort.Slice(order, func(i, j int) bool {
		a, b := r.docs[order[i]], r.docs[order[j]]
		if !a.ingestedAt.Equal(b.ingestedAt) {
			return a.ingestedAt.Before(b.ingestedAt)
		}
		if !a.observedAt.Equal(b.observedAt) {
			return a.observedAt.Before(b.observedAt)
		}
		return a.id < b.id
	})
	w := make(map[int]float64, len(order))
	for rank, d := range order {
		frac := 1.0
		if len(order) > 1 {
			frac = float64(rank) / float64(len(order)-1)
		}
		w[d] = 1 + frac
	}
	return w
}

func (r rawRetriever) retrieve(_ context.Context, q question) ([]string, error) {
	visDocs := make(map[int]bool, len(r.docs))
	var vis []int
	for d := range r.docs {
		if r.visible(d, q) {
			visDocs[d] = true
			vis = append(vis, d)
		}
	}
	var chunks []rawChunk
	for _, c := range r.chunks {
		if visDocs[c.doc] {
			chunks = append(chunks, c)
		}
	}
	scores := bm25(chunks, tokenize(q.Text))
	if r.recency {
		w := r.recencyWeights(vis)
		for i, c := range chunks {
			scores[i] *= w[c.doc]
		}
	}

	idx := make([]int, 0, len(chunks))
	for i := range chunks {
		// A chunk sharing no term with the question is not retrieved, as a
		// search engine would not return it.
		if scores[i] > 0 {
			idx = append(idx, i)
		}
	}
	// Stable: equal scores keep corpus order, so ties never depend on map
	// iteration or sort internals.
	sort.SliceStable(idx, func(a, b int) bool { return scores[idx[a]] > scores[idx[b]] })
	if len(idx) > q.Limit {
		idx = idx[:q.Limit]
	}
	out := make([]string, len(idx))
	for i, j := range idx {
		out[i] = chunks[j].text
	}
	return out, nil
}

// bm25 scores every chunk against the query terms. IDF is computed over the
// chunks visible to this question: the corpus as it stood for the question.
func bm25(chunks []rawChunk, query []string) []float64 {
	scores := make([]float64, len(chunks))
	if len(chunks) == 0 {
		return scores
	}
	var totalLen int
	df := make(map[string]int)
	for _, c := range chunks {
		totalLen += len(c.tokens)
		seen := make(map[string]bool, len(c.tokens))
		for _, t := range c.tokens {
			if !seen[t] {
				seen[t] = true
				df[t]++
			}
		}
	}
	avgLen := float64(totalLen) / float64(len(chunks))
	n := float64(len(chunks))

	// Distinct query terms in query order. The per-chunk sum runs over this
	// slice, never over a map: float addition is not associative, so summing
	// in map-iteration order let two chunks with the same terms score a bit
	// apart, and which one won the tie changed from run to run.
	terms := make(map[string]bool, len(query))
	var order []string
	for _, t := range query {
		if !terms[t] {
			terms[t] = true
			order = append(order, t)
		}
	}
	for i, c := range chunks {
		tf := make(map[string]int, len(c.tokens))
		for _, t := range c.tokens {
			if terms[t] {
				tf[t]++
			}
		}
		var s float64
		for _, t := range order {
			f, ok := tf[t]
			if !ok {
				continue
			}
			idf := math.Log(1 + (n-float64(df[t])+0.5)/(float64(df[t])+0.5))
			ff := float64(f)
			s += idf * ff * (bm25K1 + 1) / (ff + bm25K1*(1-bm25B+bm25B*float64(len(c.tokens))/avgLen))
		}
		scores[i] = s
	}
	return scores
}

// tokenize lowercases and splits on anything that is not a letter or digit.
// No stop-word list and no stemming: the baseline is plain lexical retrieval,
// and BM25's IDF already discounts common words.
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// splitSentences splits text into lines, then each line at a sentence
// terminator followed by whitespace.
func splitSentences(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		runes := []rune(line)
		start := 0
		for i, r := range runes {
			end := i == len(runes)-1
			if (r == '.' || r == '!' || r == '?') && (end || unicode.IsSpace(runes[i+1])) {
				if s := strings.TrimSpace(string(runes[start : i+1])); s != "" {
					out = append(out, s)
				}
				start = i + 1
			}
		}
		if s := strings.TrimSpace(string(runes[start:])); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// BaselineVerdict is the outcome of one metric, Mnemos against a baseline.
type BaselineVerdict string

const (
	// BaselineMnemosBetter — Mnemos moved the metric in its improving
	// direction relative to the baseline.
	BaselineMnemosBetter BaselineVerdict = "mnemos-better"
	// BaselineBetter — the baseline did better. Reported as loudly.
	BaselineBetter BaselineVerdict = "baseline-better"
	// BaselineTie — equal within the float epsilon.
	BaselineTie BaselineVerdict = "tie"
	// BaselineUnknown — either side could not be measured.
	BaselineUnknown BaselineVerdict = "unknown"
)

// BaselineComparison is one metric, Mnemos (untreated arm) against a baseline.
type BaselineComparison struct {
	Name      string    `json:"name"`
	Direction Direction `json:"direction"`
	Unit      string    `json:"unit,omitempty"`
	N         int       `json:"n,omitempty"`

	Mnemos   Metric `json:"mnemos"`
	Baseline Metric `json:"baseline"`
	// Delta is Mnemos minus baseline.
	Delta   float64         `json:"delta,omitempty"`
	Verdict BaselineVerdict `json:"verdict"`
	Note    string          `json:"note,omitempty"`
}

// BaselineSummary counts baseline comparison outcomes.
type BaselineSummary struct {
	MnemosBetter   int `json:"mnemos_better"`
	BaselineBetter int `json:"baseline_better"`
	Tie            int `json:"tie"`
	Unknown        int `json:"unknown"`
}

func (s *BaselineSummary) add(o BaselineSummary) {
	s.MnemosBetter += o.MnemosBetter
	s.BaselineBetter += o.BaselineBetter
	s.Tie += o.Tie
	s.Unknown += o.Unknown
}

// BaselineResult is one baseline's scores on a scenario and its comparison with
// the untreated Mnemos arm.
type BaselineResult struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Metrics     MetricSet            `json:"metrics"`
	Comparisons []BaselineComparison `json:"comparisons"`
	Summary     BaselineSummary      `json:"summary"`
}

// baselineDescriptions say what each baseline is, inside the report.
var baselineDescriptions = map[string]string{
	BaselineBM25: "BM25 (k1=1.2, b=0.75) over sentence chunks of the raw event texts; " +
		"time-travel questions filter documents by ingest time (recorded) or observed time (valid)",
	BaselineBM25Recency: "raw_bm25 with each chunk's score multiplied by 1 + its document's " +
		"chronological rank in [0, 1] (newest document x2)",
}

// readEvents loads the seed's raw events, the baseline's whole corpus.
func readEvents(ctx context.Context, dsn string) ([]domain.Event, error) {
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("brainbench: open seed for baseline: %w", err)
	}
	defer func() { _ = conn.Close() }()
	events, err := conn.Events.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("brainbench: list events for baseline: %w", err)
	}
	return events, nil
}

// runBaselines scores every baseline on qs and compares each with mnemos, the
// untreated arm's measurement.
func runBaselines(ctx context.Context, events []domain.Event, qs questionSet, mnemosArm MetricSet) ([]BaselineResult, error) {
	var out []BaselineResult
	for _, b := range []struct {
		name    string
		recency bool
	}{{BaselineBM25, false}, {BaselineBM25Recency, true}} {
		bat, err := runBattery(ctx, newRawRetriever(events, b.recency), qs)
		if err != nil {
			return nil, fmt.Errorf("brainbench: baseline %s: %w", b.name, err)
		}
		res := BaselineResult{
			Name:        b.name,
			Description: baselineDescriptions[b.name],
			Metrics:     MetricSet{Metrics: bat.metrics, Questions: bat.asked},
		}
		res.Comparisons, res.Summary = compareWithBaseline(mnemosArm, res.Metrics)
		out = append(out, res)
	}
	return out, nil
}

// compareWithBaseline diffs every metric the baseline measures against the
// same metric on the Mnemos arm. Metrics the baseline cannot have (census,
// vitals, gold survival) are not compared.
func compareWithBaseline(mnemosArm, base MetricSet) ([]BaselineComparison, BaselineSummary) {
	var cs []BaselineComparison
	var sum BaselineSummary
	for _, bm := range base.Metrics {
		mm, ok := mnemosArm.Get(bm.Name)
		if !ok {
			mm = UnknownMetric(bm.Name, bm.Direction, "not measured on the Mnemos arm")
		}
		c := Compare(bm, mm)
		bc := BaselineComparison{
			Name: c.Name, Direction: c.Direction, Unit: c.Unit, N: c.N,
			Mnemos: mm, Baseline: bm, Delta: c.Delta, Note: c.Note,
		}
		switch c.Verdict {
		case VerdictBetter:
			bc.Verdict = BaselineMnemosBetter
			sum.MnemosBetter++
		case VerdictWorse:
			bc.Verdict = BaselineBetter
			sum.BaselineBetter++
		case VerdictUnchanged:
			bc.Verdict = BaselineTie
			sum.Tie++
		default:
			bc.Verdict = BaselineUnknown
			sum.Unknown++
		}
		cs = append(cs, bc)
	}
	return cs, sum
}
