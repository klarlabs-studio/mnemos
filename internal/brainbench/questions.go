package brainbench

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.klarlabs.de/mnemos"
)

// The question battery is shared by every system the harness scores: each
// Mnemos arm and each raw-retrieval baseline. Building the questions once and
// scoring every answerer through the same function is what makes "Mnemos vs
// raw retrieval" a comparison rather than two numbers that happen to share a
// name: same questions, same top-k, same instants, same gold, same arithmetic.

// Metric names for the temporal-reconstruction track.
const (
	// MetricAsOfRecorded is the share of recorded-as-of probes answered
	// correctly: "what did the brain believe at T?".
	MetricAsOfRecorded = "as_of_recorded_accuracy"
	// MetricAsOfValid is the share of valid-as-of probes answered correctly:
	// "what is known now about T?".
	MetricAsOfValid = "as_of_valid_accuracy"

	// MetricClassAccuracyPrefix prefixes the per-class accuracy metrics:
	// accuracy_<class> is the share of a class's probes (ordinary and as-of
	// alike) answered correctly by the strict rule — expected text in the
	// results and no forbidden text.
	MetricClassAccuracyPrefix = "accuracy_"
)

// question is one retrieval request plus its gold.
type question struct {
	ID      string
	Text    string
	Limit   int
	Expect  string
	MustNot []string
	Class   string

	// RecordedAsOf and AsOf are zero for an ordinary probe. An as-of probe
	// sets exactly one of them.
	RecordedAsOf time.Time
	AsOf         time.Time
}

// retriever answers a question with ranked texts, best first.
type retriever interface {
	retrieve(ctx context.Context, q question) ([]string, error)
}

// questionSet is the scenario's full battery, resolved against the seed's
// phase instants.
type questionSet struct {
	probes []question
	asOf   []question
}

// buildQuestions resolves the scenario's probes into questions.
func buildQuestions(sc Scenario, phases PhaseTimes) (questionSet, error) {
	var qs questionSet
	for _, p := range sc.Probes {
		qs.probes = append(qs.probes, question{
			ID: p.ID, Text: p.Query, Limit: limitOr(p.Limit), Expect: p.Expect, MustNot: p.MustNot, Class: p.Class,
		})
	}
	for _, p := range sc.AsOfProbes {
		q := question{ID: p.ID, Text: p.Query, Limit: limitOr(p.Limit), Expect: p.Expect, MustNot: p.MustNot, Class: p.Class}
		phase := p.RecordedAsOf + p.ValidAsOf
		w, ok := phases[phase]
		if !ok || w.End.IsZero() {
			return questionSet{}, fmt.Errorf("brainbench: as-of probe %s: phase %q has no recorded instant", p.ID, phase)
		}
		if p.RecordedAsOf != "" {
			q.RecordedAsOf = w.End
		} else {
			q.AsOf = w.End
		}
		qs.asOf = append(qs.asOf, q)
	}
	return qs, nil
}

func limitOr(limit int) int {
	if limit == 0 {
		return ProbeDefaultLimit
	}
	return limit
}

// battery is one answerer's scored responses to a question set.
type battery struct {
	// asked lists the question ids in the order they were put to the
	// answerer. Tests compare it across answerers.
	asked   []string
	metrics []Metric
	// latencies holds each question's wall-clock retrieval time, in asking
	// order. Cost only: wall time varies run to run, so it is never a metric.
	latencies []time.Duration
	outcomes  []Outcome
}

// answered is one question with the texts the answerer returned for it.
type answered struct {
	q     question
	texts []string
	took  time.Duration
}

// answerLog times every retrieval and keeps what it returned, so
// the per-class tally and the cost report read the very answers the scored
// metrics were computed from rather than asking a second time (Mnemos recall
// writes back, so a second ask would not be the same question).
type answerLog struct {
	r   retriever
	log *[]answered
}

func (rr answerLog) retrieve(ctx context.Context, q question) ([]string, error) {
	began := time.Now()
	texts, err := rr.r.retrieve(ctx, q)
	if err != nil {
		return nil, err
	}
	*rr.log = append(*rr.log, answered{q: q, texts: texts, took: time.Since(began)})
	return texts, nil
}

// runBattery puts every question to r and scores the answers.
//
// As-of questions go first. They are side-effect free on Mnemos (ADR 0036 §2),
// while ordinary recall writes back; asking them first keeps the probes'
// write-backs out of the reconstruction being scored.
func runBattery(ctx context.Context, r retriever, qs questionSet) (battery, error) {
	var b battery
	var log []answered
	rec := answerLog{r: r, log: &log}
	asOf, err := scoreAsOf(ctx, rec, qs.asOf, &b.asked)
	if err != nil {
		return battery{}, err
	}
	answer, err := scoreAnswers(ctx, rec, qs.probes, &b.asked)
	if err != nil {
		return battery{}, err
	}
	classes := scoreClasses(log)
	b.metrics = make([]Metric, 0, len(answer)+len(asOf)+len(classes))
	b.metrics = append(b.metrics, answer...)
	b.metrics = append(b.metrics, asOf...)
	b.metrics = append(b.metrics, classes...)
	for _, a := range log {
		b.latencies = append(b.latencies, a.took)
		b.outcomes = append(b.outcomes, Outcome{
			ID: a.q.ID, Class: a.q.Class, Rank: rankOf(a.texts, a.q.Expect),
			Forbidden: anyForbidden(a.texts, a.q.MustNot), Correct: correct(a.texts, a.q),
		})
	}
	return b, nil
}

// Outcome is how one answerer did on one question, so a class accuracy can be
// traced back to the probes behind it.
type Outcome struct {
	ID    string `json:"id"`
	Class string `json:"class,omitempty"`
	// Rank is the 1-based rank of the first result containing the expected
	// text, 0 when it was not returned.
	Rank int `json:"rank"`
	// Forbidden is true when any must_not text was returned.
	Forbidden bool `json:"forbidden,omitempty"`
	// Correct applies the strict rule: returned, and nothing forbidden.
	Correct bool `json:"correct"`
}

// correct is the strict rule: the expected text is returned and no forbidden
// text is. Returning the current owner beside the previous one is not an
// answer to "who owns it now".
func correct(texts []string, q question) bool {
	return rankOf(texts, q.Expect) > 0 && !anyForbidden(texts, q.MustNot)
}

// scoreClasses emits one accuracy_<class> metric per class declared in the
// battery, in name order. Probes without a class are not tallied, and a
// scenario that declares no class emits nothing: there is no class whose
// accuracy is unknown, only classes nobody asked about.
func scoreClasses(log []answered) []Metric {
	n := make(map[string]int)
	ok := make(map[string]int)
	for _, a := range log {
		if a.q.Class == "" {
			continue
		}
		n[a.q.Class]++
		if correct(a.texts, a.q) {
			ok[a.q.Class]++
		}
	}
	names := make([]string, 0, len(n))
	for c := range n {
		names = append(names, c)
	}
	sort.Strings(names)
	out := make([]Metric, 0, len(names))
	for _, c := range names {
		out = append(out, KnownMetric(MetricClassAccuracyPrefix+c, HigherBetter, ratio(ok[c], n[c]), n[c], "ratio"))
	}
	return out
}

// rankOf returns the 1-based rank of the first text containing expect, or 0.
func rankOf(texts []string, expect string) int {
	for i, t := range texts {
		if containsFold(t, expect) {
			return i + 1
		}
	}
	return 0
}

func anyForbidden(texts []string, mustNot []string) bool {
	if len(mustNot) == 0 {
		return false
	}
	for _, t := range texts {
		if matchesAnyFold(t, mustNot) {
			return true
		}
	}
	return false
}

// scoreAnswers computes the answer-quality metrics over ordinary probes.
func scoreAnswers(ctx context.Context, r retriever, qs []question, asked *[]string) ([]Metric, error) {
	n := len(qs)
	var hits, top1, forbiddenHits, forbiddenProbes int
	var rrSum float64
	for _, q := range qs {
		texts, err := r.retrieve(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("brainbench: probe %s: %w", q.ID, err)
		}
		*asked = append(*asked, q.ID)
		if rank := rankOf(texts, q.Expect); rank > 0 {
			hits++
			rrSum += 1 / float64(rank)
			if rank == 1 {
				top1++
			}
		}
		if len(q.MustNot) > 0 {
			forbiddenProbes++
			if anyForbidden(texts, q.MustNot) {
				forbiddenHits++
			}
		}
	}
	out := []Metric{
		KnownMetric(MetricAnswerHitRate, HigherBetter, ratio(hits, n), n, "ratio"),
		KnownMetric(MetricAnswerPrecision1, HigherBetter, ratio(top1, n), n, "ratio"),
		KnownMetric(MetricAnswerMRR, HigherBetter, rrSum/float64(max(n, 1)), n, "ratio"),
	}
	if forbiddenProbes > 0 {
		out = append(out, KnownMetric(MetricForbiddenHitRate, LowerBetter,
			ratio(forbiddenHits, forbiddenProbes), forbiddenProbes, "ratio"))
	} else {
		// Emitting 0.0 here would read as "the brain never surfaced a
		// forbidden statement", which is not what was measured — nothing was
		// measured, because no probe declared one.
		out = append(out, UnknownMetric(MetricForbiddenHitRate, LowerBetter,
			"no probe in this scenario declares must_not; nothing was measured"))
	}
	return out, nil
}

// scoreAsOf computes one accuracy per time axis. A probe is correct when its
// expected text is in the results and no forbidden text is: a reconstruction
// that returns Tuesday's diagnosis beside Thursday's root cause did not
// reconstruct Tuesday.
func scoreAsOf(ctx context.Context, r retriever, qs []question, asked *[]string) ([]Metric, error) {
	var recN, recOK, valN, valOK int
	for _, q := range qs {
		texts, err := r.retrieve(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("brainbench: as-of probe %s: %w", q.ID, err)
		}
		*asked = append(*asked, q.ID)
		right := correct(texts, q)
		if !q.RecordedAsOf.IsZero() {
			recN++
			if right {
				recOK++
			}
			continue
		}
		valN++
		if right {
			valOK++
		}
	}
	return []Metric{
		axisMetric(MetricAsOfRecorded, recOK, recN, "recorded_as_of"),
		axisMetric(MetricAsOfValid, valOK, valN, "valid_as_of"),
	}, nil
}

func axisMetric(name string, ok, n int, key string) Metric {
	if n == 0 {
		// 0.0 would read as "every reconstruction was wrong"; nothing was
		// asked, so nothing is known.
		return UnknownMetric(name, HigherBetter, "no as-of probe in this scenario declares "+key+"; nothing was measured")
	}
	return KnownMetric(name, HigherBetter, ratio(ok, n), n, "ratio")
}

// mnemosRetriever answers through the public library facade — the same Recall
// every consuming product calls.
type mnemosRetriever struct{ mem mnemos.Memory }

func (m mnemosRetriever) retrieve(ctx context.Context, q question) ([]string, error) {
	res, err := m.mem.Recall(ctx, mnemos.Query{
		Text: q.Text, Limit: q.Limit, AsOf: q.AsOf, RecordedAsOf: q.RecordedAsOf,
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, len(res))
	for i, r := range res {
		out[i] = r.Text
	}
	return out, nil
}
