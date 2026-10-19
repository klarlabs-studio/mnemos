package mnemos

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"

	"go.klarlabs.de/mnemos/internal/ports"
	_ "go.klarlabs.de/mnemos/internal/store/memory"
	"go.klarlabs.de/mnemos/internal/trust"
)

// decayMem opens an isolated in-memory brain. Each test gets its own namespace
// so one test's beliefs never age into another's vitals.
func decayMem(t *testing.T, namespace string) *memory {
	t.Helper()
	for _, k := range []string{"MNEMOS_STORAGE", "MNEMOS_MODE", "MNEMOS_LLM_PROVIDER", "MNEMOS_API_KEY"} {
		t.Setenv(k, "")
	}
	mem, err := New(WithStorage("memory://"+namespace), WithPassiveMode(), WithActor("tester"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	return mem.(*memory)
}

// decaySeq keeps seeded event ids unique across every belief a test writes.
var decaySeq int

// rememberAged writes a belief through the real write path — a real event at a
// real past instant, a real claim linked to it as evidence — and returns its id.
//
// Confidence is 0.8 rather than the ClaimItem default of 1.0 deliberately: at
// confidence 1.0 the freshness floor (0.3) lands exactly ON the low-trust floor
// (0.30), so a fully-confident belief mathematically never decays through it.
// 0.8 is the order of magnitude extraction actually assigns.
func rememberAged(t *testing.T, m *memory, text string, age time.Duration) string {
	t.Helper()
	ctx := context.Background()
	at := time.Now().UTC().Add(-age)
	decaySeq++
	evID := fmt.Sprintf("ev-decay-%d", decaySeq)
	if err := m.RememberEvent(ctx, Event{ID: evID, At: at, Type: "observation", Content: text}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}
	id, err := m.RememberClaim(ctx, ClaimItem{
		Text: text, Confidence: 0.8, EventIDs: []string{evID}, ValidFrom: at,
	})
	if err != nil {
		t.Fatalf("RememberClaim: %v", err)
	}
	return id
}

// rescoreTrust runs the same trust recomputation production runs (pipeline's
// defaultTrustScorer / Consolidate's renormalisation pass) so the trust scores
// the vital reads are the store's own, derived from the stored evidence — not
// numbers a test wrote by hand.
func rescoreTrust(t *testing.T, m *memory) {
	t.Helper()
	scorer, ok := m.conn.Claims.(ports.TrustScorer)
	if !ok {
		t.Fatal("store does not score trust; the vital under test would be meaningless")
	}
	now := time.Now().UTC()
	if _, err := scorer.RecomputeTrust(context.Background(), domain.TrustScoring{Score: func(in domain.TrustInput) float64 {
		confidence, evidenceCount, latestEvidence := in.Confidence, in.EvidenceCount, in.LatestEvidence
		return trust.Score(confidence, evidenceCount, latestEvidence, now)
	}}); err != nil {
		t.Fatalf("recompute trust: %v", err)
	}
}

func trustDecay(t *testing.T, m *memory) Vital {
	t.Helper()
	h, err := m.BrainHealth(context.Background())
	if err != nil {
		t.Fatalf("BrainHealth: %v", err)
	}
	v := vitalByName(h, "trust_decay")
	if v.Name == "MISSING" {
		t.Fatal("trust_decay vital is not reported at all")
	}
	return v
}

// A brain whose beliefs were all just written is not losing trust: nothing sits
// close enough to the floor to fall through it inside the horizon.
func TestTrustDecay_FreshBrainIsNotDecaying(t *testing.T) {
	m := decayMem(t, "decay-fresh")
	for i := 0; i < 5; i++ {
		rememberAged(t, m, fmt.Sprintf("the ledger reconciles nightly in region %d", i), 5*24*time.Hour)
	}
	rescoreTrust(t, m)

	v := trustDecay(t, m)
	if v.Value != 0 {
		t.Errorf("trust_decay over freshly-written beliefs = %v, want 0 (%s)", v.Value, v.Detail)
	}
	if v.Status != HealthOK {
		t.Errorf("status = %q, want %q", v.Status, HealthOK)
	}
}

// floorAgeDefault is how old a default-τ belief is when its freshness reaches
// the floor: τ·ln(1/floor), ~108 days.
var floorAgeDefault = trust.FreshnessHalfLifeDays * math.Log(1/trust.FreshnessFloor)

// #481: a brain fed at a steady rate is doing what it should, and has to read
// healthy. Under the old fixed 0.20 share it read unhealthy by construction:
// ~30 of every 108 days' worth of beliefs reach the floor in any month. As a
// ratio to the steady-state rate it reads ~1.0.
func TestTrustDecay_SteadyFlowReadsAboutOne(t *testing.T) {
	m := decayMem(t, "decay-steady")
	const n = 40
	for i := 0; i < n; i++ {
		age := (float64(i) + 0.5) * floorAgeDefault / n
		rememberAged(t, m, fmt.Sprintf("the ledger reconciles nightly in region %d", i), time.Duration(age*24*float64(time.Hour)))
	}

	v := trustDecay(t, m)
	if v.Value < 0.8 || v.Value > 1.2 {
		t.Errorf("trust_decay over a steady flow = %v, want ~1.0 (%s)", v.Value, v.Detail)
	}
	if v.Status != HealthOK {
		t.Errorf("a steadily fed brain must read %q, got %q (%s)", HealthOK, v.Status, v.Detail)
	}
}

// The vital has to move with the data rather than flip: a brain whose intake
// stalled has most of its beliefs aging toward the floor together, and the
// further the crossings outrun the steady rate, the worse the grade.
func TestTrustDecay_StalledIntakeRaisesTheRatio(t *testing.T) {
	m := decayMem(t, "decay-stalled")
	const fresh = 5 * 24 * time.Hour
	const aging = 85 * 24 * time.Hour // reaches the floor (~108 days) within 30
	for i := 0; i < 10; i++ {
		rememberAged(t, m, fmt.Sprintf("the ledger reconciles nightly in region %d", i), fresh)
	}
	for i := 0; i < 20; i++ {
		rememberAged(t, m, fmt.Sprintf("the settlement batch starts at hour %d", i), aging)
	}

	backlog := trustDecay(t, m)
	if backlog.Status != HealthDegraded {
		t.Errorf("20 of 30 aging out together = %v should warn, got %q (%s)", backlog.Value, backlog.Status, backlog.Detail)
	}

	for i := 0; i < 60; i++ {
		rememberAged(t, m, fmt.Sprintf("the export worker drains queue %d", i), aging)
	}
	stalled := trustDecay(t, m)
	if !(stalled.Value > backlog.Value) {
		t.Fatalf("more beliefs aging out together must raise the ratio: %v then %v", backlog.Value, stalled.Value)
	}
	if stalled.Status != HealthUnhealthy {
		t.Errorf("80 of 90 aging out together = %v should be critical, got %q (%s)", stalled.Value, stalled.Status, stalled.Detail)
	}
}

// The point of the vital: it reads each belief's OWN half-life. Two beliefs of
// identical age, identical evidence and identical confidence decay at different
// rates once one is declared volatile — and only that one is flagged. The
// fixed-horizon staleness count cannot tell them apart, which is exactly why
// this is a separate vital and not a second reading of that one.
func TestTrustDecay_HonoursPerClaimHalfLife(t *testing.T) {
	m := decayMem(t, "decay-halflife")
	ctx := context.Background()
	const age = 5 * 24 * time.Hour
	at := time.Now().UTC().Add(-age)

	volatile := rememberAged(t, m, "the export worker runs on the batch queue", age)
	durable := rememberAged(t, m, "the export format was chosen for downstream parsers", age)
	// The same path `mnemos verify --half-life` takes: re-verify at the same
	// instant, but declare different decay profiles.
	if err := m.conn.Claims.MarkVerified(ctx, volatile, at, 14); err != nil {
		t.Fatalf("MarkVerified volatile: %v", err)
	}
	if err := m.conn.Claims.MarkVerified(ctx, durable, at, 365); err != nil {
		t.Fatalf("MarkVerified durable: %v", err)
	}
	rescoreTrust(t, m)

	// Precondition: the two differ ONLY in declared half-life. Their stored
	// trust scores are equal, so anything the vital reports about one and not
	// the other can only come from the decay model.
	claims, err := m.conn.Claims.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]float64{}
	for _, c := range claims {
		byID[c.ID] = c.TrustScore
	}
	if !approx(byID[volatile], byID[durable], 1e-6) {
		t.Fatalf("precondition: stored trust must be equal, got %v and %v", byID[volatile], byID[durable])
	}

	// Only the volatile belief reaches the floor within the horizon, and the
	// steady-state expectation is built from each belief's own time constant:
	// a 14-day belief always crosses within a month, a 365-day one rarely.
	h, err := m.BrainHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v := vitalByName(h, "trust_decay")
	if !strings.HasPrefix(v.Detail, "1 of 2 beliefs") {
		t.Errorf("only the volatile belief should reach the floor: %s", v.Detail)
	}
	expected := 1 + healthTrustDecayHorizonDays/(365*math.Log(1/trust.FreshnessFloor))
	if !approx(v.Value, 1/expected, 1e-6) {
		t.Errorf("trust_decay = %v, want %v: crossings over expectations from each belief's own τ (%s)", v.Value, 1/expected, v.Detail)
	}
}

// #325's standard: a vital with nothing behind it reports unknown, not a
// reassuring zero. An empty brain has no belief whose decay could be measured.
func TestTrustDecay_EmptyBrainIsUnknown(t *testing.T) {
	m := decayMem(t, "decay-empty")
	v := trustDecay(t, m)
	if v.Status != HealthUnknown {
		t.Errorf("trust_decay on an empty brain = %q, want %q", v.Status, HealthUnknown)
	}
	h, err := m.BrainHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != HealthOK {
		t.Errorf("an unmeasured vital must not drag the verdict to %q", h.Status)
	}
}

// A brain whose beliefs have all aged onto the freshness floor has nothing left
// on its way there, so trust_decay is unknown, not a reassuring zero. Aging is
// not untrust (ADR 0032): low_trust stays clear, and staleness is the vital
// that raises the alarm.
func TestTrustDecay_AgedOutBrainIsUnknownAndStalenessCatchesIt(t *testing.T) {
	m := decayMem(t, "decay-spent")
	for i := 0; i < 4; i++ {
		rememberAged(t, m, fmt.Sprintf("the importer accepted fixed-width files in region %d", i), 3*365*24*time.Hour)
	}
	rescoreTrust(t, m)

	h, err := m.BrainHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v := vitalByName(h, "trust_decay"); v.Status != HealthUnknown {
		t.Errorf("trust_decay with nothing left on its way to the floor = %q, want %q (%s)", v.Status, HealthUnknown, v.Detail)
	}
	if lt := vitalByName(h, "low_trust"); lt.Value != 0 {
		t.Errorf("aged-out beliefs are stale, not untrusted: low_trust = %+v", lt)
	}
	if st := vitalByName(h, "staleness"); st.Status != HealthUnhealthy {
		t.Errorf("staleness must own the aged-out state, got %+v", st)
	}
}
