package mnemos

import (
	"context"
	"math"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// calibMem builds an in-memory Memory and returns the concrete *memory so the
// test can seed claims + outcome edges directly (the decision→outcome→attach
// flow that mints validates/refutes edges isn't on the public API).
func calibMem(t *testing.T) *memory {
	t.Helper()
	for _, k := range []string{"MNEMOS_STORAGE", "MNEMOS_MODE", "MNEMOS_LLM_PROVIDER", "MNEMOS_API_KEY"} {
		t.Setenv(k, "")
	}
	mem, err := New(WithStorage("memory://?namespace=calibration"), WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	return mem.(*memory)
}

func seedClaim(t *testing.T, m *memory, id string, confidence float64) {
	t.Helper()
	seedClaimFrom(t, m, id, confidence, "")
}

func seedClaimFrom(t *testing.T, m *memory, id string, confidence float64, source string) {
	t.Helper()
	now := time.Now().UTC()
	if err := m.conn.Claims.Upsert(context.Background(), []domain.Claim{{
		ID: id, Text: "claim " + id, Type: domain.ClaimTypeFact,
		Confidence: confidence, Status: domain.ClaimStatusActive,
		CreatedBy: source, CreatedAt: now, ValidFrom: now,
	}}); err != nil {
		t.Fatalf("seed claim %s: %v", id, err)
	}
}

// adjudicate mints an outcome→claim validates (correct) / refutes (wrong) edge.
func adjudicate(t *testing.T, m *memory, edgeID, claimID string, correct bool, at time.Time) {
	t.Helper()
	kind := domain.RelationshipTypeRefutes
	if correct {
		kind = domain.RelationshipTypeValidates
	}
	if err := m.conn.EntityRels.Upsert(context.Background(), []domain.EntityRelationship{{
		ID: edgeID, Kind: kind,
		FromID: "out_" + edgeID, FromType: domain.RelEntityOutcome,
		ToID: claimID, ToType: domain.RelEntityClaim,
		CreatedAt: at,
	}}); err != nil {
		t.Fatalf("adjudicate %s: %v", claimID, err)
	}
}

func approx(a, b, eps float64) bool { return math.Abs(a-b) < eps }

func TestCalibration_CurveECEBrier(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Bucket [0.9,1.0): 4 claims at 0.9 confidence, 3 borne out, 1 wrong →
	// stated 0.9 but only 75% right (overconfident).
	for i, id := range []string{"c1", "c2", "c3", "c4"} {
		seedClaim(t, m, id, 0.9)
		adjudicate(t, m, id, id, i < 3, now)
	}
	// Bucket [0.5,0.6): 2 claims at 0.5, 1 right 1 wrong → perfectly calibrated.
	seedClaim(t, m, "c5", 0.5)
	adjudicate(t, m, "c5", "c5", true, now)
	seedClaim(t, m, "c6", 0.5)
	adjudicate(t, m, "c6", "c6", false, now)

	cal, err := m.Calibration(ctx)
	if err != nil {
		t.Fatalf("Calibration: %v", err)
	}
	if cal.Samples != 6 {
		t.Fatalf("Samples = %d, want 6", cal.Samples)
	}
	if len(cal.Buckets) != 2 {
		t.Fatalf("Buckets = %d, want 2: %+v", len(cal.Buckets), cal.Buckets)
	}
	byLower := map[float64]CalibrationBucket{}
	for _, b := range cal.Buckets {
		byLower[b.Lower] = b
	}
	hi := byLower[0.9]
	if hi.Count != 4 || !approx(hi.MeanConfidence, 0.9, 1e-9) || !approx(hi.Accuracy, 0.75, 1e-9) {
		t.Fatalf("0.9 bucket = %+v, want count4 conf0.9 acc0.75", hi)
	}
	lo := byLower[0.5]
	if lo.Count != 2 || !approx(lo.Accuracy, 0.5, 1e-9) {
		t.Fatalf("0.5 bucket = %+v, want count2 acc0.5", lo)
	}
	// ECE = (4/6)*|0.75-0.9| + (2/6)*0 = 0.1
	if !approx(cal.ECE, 0.1, 1e-9) {
		t.Fatalf("ECE = %.4f, want 0.1", cal.ECE)
	}
	// Brier = (3*0.01 + 0.81 + 0.25 + 0.25)/6
	if !approx(cal.Brier, 1.34/6.0, 1e-9) {
		t.Fatalf("Brier = %.4f, want %.4f", cal.Brier, 1.34/6.0)
	}
}

func TestCalibration_LatestVerdictWins(t *testing.T) {
	m := calibMem(t)
	base := time.Now().UTC()
	seedClaim(t, m, "c1", 0.8)
	// First an outcome validates it, then a LATER outcome refutes it → wrong.
	adjudicate(t, m, "e_early", "c1", true, base)
	adjudicate(t, m, "e_late", "c1", false, base.Add(time.Hour))

	cal, err := m.Calibration(context.Background())
	if err != nil {
		t.Fatalf("Calibration: %v", err)
	}
	if cal.Samples != 1 {
		t.Fatalf("Samples = %d, want 1 (one claim, latest verdict)", cal.Samples)
	}
	if len(cal.Buckets) != 1 || cal.Buckets[0].Accuracy != 0 {
		t.Fatalf("want the later refutation to win (accuracy 0), got %+v", cal.Buckets)
	}
}

func TestCalibration_PerSource(t *testing.T) {
	m := calibMem(t)
	now := time.Now().UTC()
	// grace: 2 claims at 0.9, both borne out → well-under-confident (gap -0.1).
	seedClaimFrom(t, m, "g1", 0.9, "grace")
	adjudicate(t, m, "g1", "g1", true, now)
	seedClaimFrom(t, m, "g2", 0.9, "grace")
	adjudicate(t, m, "g2", "g2", true, now)
	// kai: 2 claims at 0.9, both wrong → badly over-confident (gap +0.9).
	seedClaimFrom(t, m, "k1", 0.9, "kai")
	adjudicate(t, m, "k1", "k1", false, now)
	seedClaimFrom(t, m, "k2", 0.9, "kai")
	adjudicate(t, m, "k2", "k2", false, now)

	cal, err := m.Calibration(context.Background())
	if err != nil {
		t.Fatalf("Calibration: %v", err)
	}
	if len(cal.Sources) != 2 {
		t.Fatalf("Sources = %d, want 2: %+v", len(cal.Sources), cal.Sources)
	}
	bySrc := map[string]SourceCalibration{}
	for _, s := range cal.Sources {
		bySrc[s.Source] = s
	}
	grace := bySrc["grace"]
	if grace.Samples != 2 || !approx(grace.Accuracy, 1.0, 1e-9) || !approx(grace.Gap, -0.1, 1e-9) {
		t.Fatalf("grace = %+v, want samples2 acc1.0 gap-0.1 (under-confident)", grace)
	}
	kai := bySrc["kai"]
	if kai.Samples != 2 || !approx(kai.Accuracy, 0.0, 1e-9) || !approx(kai.Gap, 0.9, 1e-9) {
		t.Fatalf("kai = %+v, want samples2 acc0 gap+0.9 (over-confident)", kai)
	}
	// A positive gap flags the over-confident source — the whole point.
	if kai.Gap <= grace.Gap {
		t.Fatal("over-confident source must have a larger gap than the under-confident one")
	}
}

func TestCalibration_EmptyIsZero(t *testing.T) {
	m := calibMem(t)
	cal, err := m.Calibration(context.Background())
	if err != nil {
		t.Fatalf("Calibration: %v", err)
	}
	if cal.Samples != 0 || len(cal.Buckets) != 0 || cal.ECE != 0 || cal.Brier != 0 {
		t.Fatalf("empty store must yield a zero Calibration, got %+v", cal)
	}
}
