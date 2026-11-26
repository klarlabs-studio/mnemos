package brainbench

import (
	"context"
	"os"
	"testing"

	"go.klarlabs.de/mnemos/internal/trust"
)

// TestRun_CapturedStreamTruthOutTrustsRepetition pins #443 on the shipped
// captured_stream scenario: a falsehood repeated in four chunks of one file and
// restated by a second session must not out-trust a truth stated once and
// borne out by one outcome. With same-origin repeats at half weight it did.
// The false belief keeps every episode as provenance; only its credibility
// stops growing with them.
func TestRun_CapturedStreamTruthOutTrustsRepetition(t *testing.T) {
	raw, err := os.ReadFile("../../data/eval/brainbench/captured_stream.yaml")
	if err != nil {
		t.Fatalf("read scenario: %v", err)
	}
	sc, err := ParseScenario(raw)
	if err != nil {
		t.Fatalf("parse scenario: %v", err)
	}
	res, err := Run(context.Background(), sc, t.TempDir())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	cr := res.Credibility
	if cr == nil || len(cr.Pairs) != 1 {
		t.Fatalf("credibility track = %+v, want one judged pair", cr)
	}
	if cr.TrustModel != trust.ModelVersion {
		t.Errorf("judged under %s, want the current model %s", cr.TrustModel, trust.ModelVersion)
	}
	if cr.OutcomesApplied != 1 || cr.Credited != 1 {
		t.Errorf("outcomes applied %d, credited %d; want the one outcome to reach credibility", cr.OutcomesApplied, cr.Credited)
	}
	p := cr.Pairs[0]
	if !p.Holds || !cr.Holds {
		t.Fatalf("the repeated falsehood out-trusts the corroborated truth: %+v", p)
	}
	// The repetition is kept as provenance: four file chunks and one session.
	if p.Lower.Episodes != 5 {
		t.Errorf("false belief has %d evidence episodes, want 5 kept as provenance", p.Lower.Episodes)
	}
	// ...and grades as two independent sources, the file and the session.
	// The file's repeats add none.
	if p.Lower.Evidence != 2 {
		t.Errorf("false belief grades %d independent sources, want 2", p.Lower.Evidence)
	}
	// Consequences, not agreement inside the stream, raise credibility.
	if p.Higher.Credit <= 0 {
		t.Errorf("true belief credit = %v, want the outcome's positive credit", p.Higher.Credit)
	}
	if p.Higher.Evidence != 1 {
		t.Errorf("true belief grades %d sources, want its one episode", p.Higher.Evidence)
	}
}
