package bias

import (
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestSourceConcentration_Fires(t *testing.T) {
	t.Parallel()
	events := make([]domain.Event, 0, 10)
	for i := 0; i < 9; i++ {
		events = append(events, domain.Event{ID: "e", SourceInputID: "doc-1"})
	}
	events = append(events, domain.Event{ID: "e", SourceInputID: "doc-2"})
	r := Analyse(AnalysisInput{Events: events}, DefaultThresholds())
	got := findingByKind(r, "source_concentration")
	if got == nil {
		t.Fatal("expected source_concentration finding")
	}
	if got.Score < 0.6 {
		t.Errorf("score = %v", got.Score)
	}
}

func TestSourceConcentration_DoesNotFireWhenBalanced(t *testing.T) {
	t.Parallel()
	events := []domain.Event{
		{SourceInputID: "a"}, {SourceInputID: "b"}, {SourceInputID: "c"}, {SourceInputID: "d"}, {SourceInputID: "e"},
	}
	r := Analyse(AnalysisInput{Events: events}, DefaultThresholds())
	if findingByKind(r, "source_concentration") != nil {
		t.Error("balanced sources should not fire")
	}
}

func TestPolaritySkew_Fires(t *testing.T) {
	t.Parallel()
	claims := []domain.Claim{
		{Text: "Deploy succeeded"}, {Text: "Migration shipped"}, {Text: "Tests passed"}, {Text: "Release approved"},
		{Text: "Cleanup completed"}, {Text: "Backfill delivered"},
	}
	r := Analyse(AnalysisInput{Claims: claims}, DefaultThresholds())
	got := findingByKind(r, "polarity_skew")
	if got == nil {
		t.Fatal("expected polarity_skew finding")
	}
}

func TestPolaritySkew_DoesNotFireOnBalance(t *testing.T) {
	t.Parallel()
	claims := []domain.Claim{
		{Text: "deploy succeeded"}, {Text: "migration failed"},
		{Text: "tests passed"}, {Text: "rollback broken"},
	}
	r := Analyse(AnalysisInput{Claims: claims}, DefaultThresholds())
	if findingByKind(r, "polarity_skew") != nil {
		t.Error("balanced polarity should not fire")
	}
}

func TestTemporalCluster_FiresOnSameInstant(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	events := []domain.Event{
		{Timestamp: t0}, {Timestamp: t0}, {Timestamp: t0}, {Timestamp: t0},
	}
	r := Analyse(AnalysisInput{Events: events}, DefaultThresholds())
	got := findingByKind(r, "temporal_cluster")
	if got == nil {
		t.Fatal("same-instant events should fire")
	}
	if got.Score != 1 {
		t.Errorf("score = %v", got.Score)
	}
}

func TestTemporalCluster_DoesNotFireOnEvenSpread(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	events := make([]domain.Event, 0, 10)
	for i := 0; i < 10; i++ {
		events = append(events, domain.Event{Timestamp: t0.Add(time.Duration(i) * time.Hour)})
	}
	r := Analyse(AnalysisInput{Events: events}, DefaultThresholds())
	if got := findingByKind(r, "temporal_cluster"); got != nil {
		t.Errorf("evenly-spread events should not fire (got score %v)", got.Score)
	}
}

func TestSingleSourceOfTruth_Fires(t *testing.T) {
	t.Parallel()
	in := AnalysisInput{
		Claims: []domain.Claim{{ID: "c1"}, {ID: "c2"}, {ID: "c3"}, {ID: "c4"}, {ID: "c5"}},
		EvidenceMap: map[string][]string{
			"c1": {"e1"}, "c2": {"e1"}, "c3": {"e1"}, "c4": {"e1"}, "c5": {"e2"},
		},
	}
	r := Analyse(in, DefaultThresholds())
	got := findingByKind(r, "single_source_of_truth")
	if got == nil {
		t.Fatal("expected single_source_of_truth")
	}
	if got.Score < 0.6 {
		t.Errorf("score = %v", got.Score)
	}
}

func TestAnalyse_EmptyInputProducesEmptyReport(t *testing.T) {
	t.Parallel()
	r := Analyse(AnalysisInput{}, DefaultThresholds())
	if r.HasFindings() {
		t.Errorf("empty input fired: %+v", r.Findings)
	}
}

func findingByKind(r Report, kind string) *Finding {
	for i := range r.Findings {
		if r.Findings[i].Kind == kind {
			return &r.Findings[i]
		}
	}
	return nil
}
