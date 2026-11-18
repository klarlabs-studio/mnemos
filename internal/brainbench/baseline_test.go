package brainbench

import (
	"context"
	"reflect"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

var phaseInstant = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// fakeRetriever answers by question id.
type fakeRetriever map[string][]string

func (f fakeRetriever) retrieve(_ context.Context, q question) ([]string, error) {
	return f[q.ID], nil
}

// recordingRetriever records every question it is asked.
type recordingRetriever struct{ asked *[]question }

func (r recordingRetriever) retrieve(_ context.Context, q question) ([]string, error) {
	*r.asked = append(*r.asked, q)
	return nil, nil
}

// TestRun_BaselineAnswersTheIdenticalQuestionSet is the baseline's validity
// check. "Mnemos vs raw retrieval" means something only if both were asked
// the same questions with the same top-k and scored on the same sample: a
// baseline silently skipping a probe would change every rate it reports.
func TestRun_BaselineAnswersTheIdenticalQuestionSet(t *testing.T) {
	for _, sc := range []Scenario{experimentScenario(Process{}), timelineScenario(t, Process{})} {
		t.Run(sc.ID, func(t *testing.T) {
			res, err := Run(context.Background(), sc, t.TempDir())
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if want := len(sc.Probes) + len(sc.AsOfProbes); len(res.Questions) != want {
				t.Fatalf("Mnemos was asked %d questions, want %d", len(res.Questions), want)
			}
			names := map[string]bool{}
			for _, b := range res.Baselines {
				names[b.Name] = true
				if !reflect.DeepEqual(b.Metrics.Questions, res.Questions) {
					t.Errorf("%s asked %v, Mnemos asked %v", b.Name, b.Metrics.Questions, res.Questions)
				}
				if len(b.Comparisons) == 0 {
					t.Errorf("%s compared no metrics", b.Name)
				}
				for _, c := range b.Comparisons {
					if c.Mnemos.Known && c.Baseline.Known && c.Mnemos.N != c.Baseline.N {
						t.Errorf("%s %s: Mnemos n=%d, baseline n=%d", b.Name, c.Name, c.Mnemos.N, c.Baseline.N)
					}
				}
			}
			for _, want := range []string{BaselineBM25, BaselineBM25Recency} {
				if !names[want] {
					t.Errorf("baseline %s missing from the result", want)
				}
			}
		})
	}
}

// TestRunBattery_AsksEveryAnswererTheSameQuestions pins the mechanism the run
// test relies on: one question set, one function, identical requests.
func TestRunBattery_AsksEveryAnswererTheSameQuestions(t *testing.T) {
	sc := timelineScenario(t, Process{})
	phases := PhaseTimes{
		"monday":   {Start: phaseInstant, End: phaseInstant.Add(time.Hour)},
		"tuesday":  {Start: phaseInstant.Add(2 * time.Hour), End: phaseInstant.Add(3 * time.Hour)},
		"thursday": {Start: phaseInstant.Add(4 * time.Hour), End: phaseInstant.Add(5 * time.Hour)},
	}
	qs, err := buildQuestions(sc, phases)
	if err != nil {
		t.Fatal(err)
	}
	var a, b []question
	if _, err := runBattery(context.Background(), recordingRetriever{&a}, qs); err != nil {
		t.Fatal(err)
	}
	if _, err := runBattery(context.Background(), recordingRetriever{&b}, qs); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) || len(a) != len(sc.Probes)+len(sc.AsOfProbes) {
		t.Fatalf("answerers were asked different questions:\n%+v\n%+v", a, b)
	}
	for _, q := range a {
		if q.Limit != ProbeDefaultLimit {
			t.Errorf("%s limit = %d, want the default %d", q.ID, q.Limit, ProbeDefaultLimit)
		}
		if q.ID == "believed-tue-cause" && !q.RecordedAsOf.Equal(phases["tuesday"].End) {
			t.Errorf("recorded-as-of resolved to %v, want tuesday's end", q.RecordedAsOf)
		}
		if q.ID == "known-tue-cause" && !q.AsOf.Equal(phases["tuesday"].End) {
			t.Errorf("valid-as-of resolved to %v, want tuesday's end", q.AsOf)
		}
	}
}

func rawEvents() []domain.Event {
	mon, tue, thu := phaseInstant, phaseInstant.Add(24*time.Hour), phaseInstant.Add(72*time.Hour)
	return []domain.Event{
		{ID: "a-diag", Content: "The outage was caused by disk saturation.", Timestamp: tue, IngestedAt: tue},
		{ID: "b-rca", Content: "The outage was caused by the pool change.\nThe pool size is 200.", Timestamp: mon, IngestedAt: thu},
	}
}

func TestRawRetriever_TimeTravelFilters(t *testing.T) {
	r := newRawRetriever(rawEvents(), false)
	ask := func(q question) []string {
		q.Text, q.Limit = "caused outage", 10
		got, err := r.retrieve(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	wed := phaseInstant.Add(48 * time.Hour)
	if got := ask(question{RecordedAsOf: wed}); len(got) != 1 || rankOf(got, "disk") != 1 {
		t.Errorf("recorded as of Wednesday = %v, want only the Tuesday diagnosis", got)
	}
	// Known now about Wednesday: the root cause was observed Monday, so a
	// timestamped log returns it — beside the diagnosis it cannot retract.
	if got := ask(question{AsOf: wed}); rankOf(got, "pool change") == 0 || rankOf(got, "disk") == 0 {
		t.Errorf("valid as of Wednesday = %v, want both documents", got)
	}
	if got := ask(question{}); len(got) != 2 {
		t.Errorf("no filter = %v, want both cause sentences and not the unrelated one", got)
	}
}

func TestRawRetriever_RecencyFavoursTheNewestDocument(t *testing.T) {
	events := []domain.Event{
		{ID: "a", Content: "The region is eu-west-1.", Timestamp: phaseInstant, IngestedAt: phaseInstant},
		{ID: "b", Content: "The region is eu-central-1.", Timestamp: phaseInstant.Add(time.Hour), IngestedAt: phaseInstant},
	}
	q := question{Text: "which region", Limit: 10}
	plain, _ := newRawRetriever(events, false).retrieve(context.Background(), q)
	recent, _ := newRawRetriever(events, true).retrieve(context.Background(), q)
	if rankOf(plain, "eu-west-1") != 1 {
		t.Errorf("plain BM25 tie should keep corpus order, got %v", plain)
	}
	if rankOf(recent, "eu-central-1") != 1 {
		t.Errorf("recency-weighted should rank the newer document first, got %v", recent)
	}
}

func TestSplitSentences(t *testing.T) {
	got := splitSentences("One is 0.5 wide. Two!\nThree? Four\n\n  ")
	want := []string{"One is 0.5 wide.", "Two!", "Three?", "Four"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCompareWithBaseline(t *testing.T) {
	mn := MetricSet{Metrics: []Metric{
		KnownMetric(MetricAnswerHitRate, HigherBetter, 1, 4, "ratio"),
		KnownMetric(MetricForbiddenHitRate, LowerBetter, 0.5, 2, "ratio"),
		KnownMetric(MetricAnswerMRR, HigherBetter, 0.5, 4, "ratio"),
		UnknownMetric(MetricAsOfValid, HigherBetter, "none"),
	}}
	base := MetricSet{Metrics: []Metric{
		KnownMetric(MetricAnswerHitRate, HigherBetter, 0.5, 4, "ratio"),
		KnownMetric(MetricForbiddenHitRate, LowerBetter, 0.5, 2, "ratio"),
		KnownMetric(MetricAnswerMRR, HigherBetter, 1, 4, "ratio"),
		UnknownMetric(MetricAsOfValid, HigherBetter, "none"),
	}}
	cs, sum := compareWithBaseline(mn, base)
	want := []BaselineVerdict{BaselineMnemosBetter, BaselineTie, BaselineBetter, BaselineUnknown}
	for i, c := range cs {
		if c.Verdict != want[i] {
			t.Errorf("%s: verdict %q, want %q", c.Name, c.Verdict, want[i])
		}
	}
	if sum != (BaselineSummary{MnemosBetter: 1, BaselineBetter: 1, Tie: 1, Unknown: 1}) {
		t.Errorf("summary = %+v", sum)
	}
	if cs[0].Delta != 0.5 {
		t.Errorf("delta = %v, want Mnemos minus baseline = 0.5", cs[0].Delta)
	}
}
