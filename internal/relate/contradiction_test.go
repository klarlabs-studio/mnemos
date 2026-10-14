package relate

import (
	"fmt"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

// contradictionVerdicts runs a pair through both entry points production uses
// (the batch pass and the incremental pass) and returns the contradiction
// edges each produced. The two must agree.
func contradictionVerdicts(t *testing.T, a, b string) []domain.Relationship {
	t.Helper()
	ca := domain.Claim{ID: "cl_a", Text: a}
	cb := domain.Claim{ID: "cl_b", Text: b}
	batch, err := equivalenceEngine().Detect([]domain.Claim{ca, cb})
	if err != nil {
		t.Fatal(err)
	}
	inc, err := equivalenceEngine().DetectIncremental([]domain.Claim{ca}, []domain.Claim{cb})
	if err != nil {
		t.Fatal(err)
	}
	pick := func(rels []domain.Relationship) []domain.Relationship {
		var out []domain.Relationship
		for _, r := range rels {
			if r.Type == domain.RelationshipTypeContradicts {
				out = append(out, r)
			}
		}
		return out
	}
	pb, pi := pick(batch), pick(inc)
	if len(pb) != len(pi) {
		t.Fatalf("batch and incremental passes disagree on %q vs %q: %d vs %d contradiction edges", a, b, len(pb), len(pi))
	}
	return pb
}

// The negative fixtures are the classes a hand-classified random sample of 20
// open contradictions on a real brain consisted of — none of them genuine:
// session-header fragments, code lines and short fragments paired with a fact,
// paraphrases that agree, and unrelated pairs.
func TestContradiction_RealBrainNoiseIsNotFlagged(t *testing.T) {
	cases := []struct{ class, a, b string }{
		{"session header", "User messages in this session:", "User messages are not persisted in this session"},
		{"session header", "User messages in this session:", "The session has no user messages yet"},
		{"boilerplate fragment", "All user messages", "User messages were not captured"},
		{"boilerplate fragment", "All user messages", "No user messages mention the deploy"},
		{"fragment", "The gate passed", "The gate did not pass on the first push"},
		{"code line", "type CompilationMachine interface {", "CompilationMachine is not an interface"},
		{"code line", "pub signing_key: String,", "The signing_key is not a String"},
		{"code line", "pub signing_key: String,", "pub signing_key: Vec<u8>,"},
		{"agreeing paraphrase", "Tests fail without the fix and pass with it", "The fix makes the test go red→green"},
		{"agreeing paraphrase", "Tests fail without the fix and pass with it", "The new test failed before the fix and passes after it"},
		{"agreeing paraphrase", "The test fails without the fix", "Without the fix the test fails"},
		{"agreeing paraphrase (negated antonym)", "Feature X is not enabled in prod", "Feature X is disabled in prod"},
		{"agreeing paraphrase (synonym swap)", "The fix makes tests pass", "The fix makes tests green"},
		{"agreeing paraphrase (both negated)", "The cache is not shared across tenants", "Tenants never share the cache"},
		{"unrelated", "Release build is still running", "The release notes were finished yesterday"},
		{"unrelated", "The gate passed after the rebase", "Docs build failed without network access"},
		{"negation word in a different sentence", "The importer does not retry on timeouts", "The importer writes one row per invoice line"},
	}
	for _, tc := range cases {
		t.Run(tc.class+": "+tc.a, func(t *testing.T) {
			if got := contradictionVerdicts(t, tc.a, tc.b); len(got) != 0 {
				t.Errorf("%s flagged as a contradiction (%s): %q vs %q", tc.class, got[0].DerivedBy, tc.a, tc.b)
			}
		})
	}
}

// Genuine conflicts: the same subject, incompatible values or polarity. Each
// names the detector expected to produce it.
func TestContradiction_GenuineConflictsAreFlagged(t *testing.T) {
	cases := []struct{ detector, a, b string }{
		{DetectorNumeric, "The deploy uses Postgres 15", "The deploy uses Postgres 16"},
		{DetectorNumeric, "the customer had 12 prior refunds", "the customer had 0 prior refunds"},
		{DetectorValue, "Feature X is enabled in prod", "Feature X is disabled in prod"},
		{DetectorValue, "The frontend uses React", "The frontend uses Vue"},
		{DetectorPolarity, "Feature X is enabled in prod", "Feature X is not enabled in prod"},
		{DetectorPolarity, "Revenue decreased after launch", "Revenue did not decrease after launch"},
		{DetectorEntity, "The CEO is Alice", "The CEO is Bob"},
		{DetectorTemporal, "migration completed Tuesday", "migration is still running"},
	}
	for _, tc := range cases {
		t.Run(tc.a+" / "+tc.b, func(t *testing.T) {
			got := contradictionVerdicts(t, tc.a, tc.b)
			if len(got) != 1 {
				t.Fatalf("want one contradiction edge, got %d", len(got))
			}
			if want := ContradictionDerivedBy(tc.detector); got[0].DerivedBy != want {
				t.Errorf("DerivedBy = %q, want %q", got[0].DerivedBy, want)
			}
			if s := got[0].Strength; s <= 0 || s > 1 {
				t.Errorf("strength = %v, want a real value in (0, 1]", s)
			}
		})
	}
}

// A detector's strength reflects how well the two claims align: a pair that
// differs only in the conflicting value outranks one that also differs
// elsewhere.
func TestContradiction_StrengthTracksAlignment(t *testing.T) {
	tight := contradictionVerdicts(t, "The cache is enabled for tenants", "The cache is not enabled for tenants")
	loose := contradictionVerdicts(t, "The cache is enabled for tenants", "The cache is not enabled for tenants in staging")
	if len(tight) != 1 || len(loose) != 1 {
		t.Fatalf("want one edge each, got %d and %d", len(tight), len(loose))
	}
	if !(tight[0].Strength > loose[0].Strength) {
		t.Errorf("tight pair strength %v should exceed loose pair strength %v", tight[0].Strength, loose[0].Strength)
	}
}

func TestIsContradictionEndpoint(t *testing.T) {
	reject := []string{
		"User messages in this session:",
		"All user messages",
		"The gate passed",
		"type CompilationMachine interface {",
		"pub signing_key: String,",
		"func (e Engine) Detect(claims []domain.Claim) ([]domain.Relationship, error) {",
		"if err != nil {",
		"x := compute(a, b)",
		"Let me check the config",
		"The changes are:",
		"",
	}
	for _, s := range reject {
		if IsContradictionEndpoint(s) {
			t.Errorf("accepted %q as a contradiction endpoint", s)
		}
	}
	accept := []string{
		"The CEO is Alice",
		"The deploy uses Postgres 15",
		"Feature X is enabled in prod",
		"Revenue did not decrease after launch",
		"The importer does not retry on timeouts",
		"migration is still running",
	}
	for _, s := range accept {
		if !IsContradictionEndpoint(s) {
			t.Errorf("rejected %q as a contradiction endpoint", s)
		}
	}
}

// One claim must not accumulate contradictions with everything that shares
// its subject: a pass keeps at most MaxContradictionsPerClaim per claim, the
// strongest first.
func TestContradiction_PerClaimCap(t *testing.T) {
	hub := domain.Claim{ID: "cl_hub", Text: "The deploy uses Postgres 15"}
	var existing []domain.Claim
	for v := 1; v <= 40; v++ {
		if v == 15 {
			continue
		}
		existing = append(existing, domain.Claim{ID: fmt.Sprintf("cl_v%02d", v), Text: fmt.Sprintf("The deploy uses Postgres %d", v)})
	}

	count := func(rels []domain.Relationship) map[string]int {
		deg := map[string]int{}
		for _, r := range rels {
			if r.Type == domain.RelationshipTypeContradicts {
				deg[r.FromClaimID]++
				deg[r.ToClaimID]++
			}
		}
		return deg
	}

	inc, stats, err := equivalenceEngine().DetectIncrementalWithStats([]domain.Claim{hub}, existing)
	if err != nil {
		t.Fatal(err)
	}
	if got := count(inc)["cl_hub"]; got != MaxContradictionsPerClaim {
		t.Errorf("incremental: hub kept %d contradictions, want %d", got, MaxContradictionsPerClaim)
	}
	if stats.ContradictionsOverCap != len(existing)-MaxContradictionsPerClaim {
		t.Errorf("ContradictionsOverCap = %d, want %d", stats.ContradictionsOverCap, len(existing)-MaxContradictionsPerClaim)
	}

	batch, err := equivalenceEngine().Detect(append([]domain.Claim{hub}, existing...))
	if err != nil {
		t.Fatal(err)
	}
	for id, d := range count(batch) {
		if d > MaxContradictionsPerClaim {
			t.Errorf("batch: %s kept %d contradictions, over the cap of %d", id, d, MaxContradictionsPerClaim)
		}
	}

	uncapped, err := equivalenceEngine().WithContradictionCap(-1).DetectIncremental([]domain.Claim{hub}, existing)
	if err != nil {
		t.Fatal(err)
	}
	if got := count(uncapped)["cl_hub"]; got != len(existing) {
		t.Errorf("uncapped: hub kept %d contradictions, want %d", got, len(existing))
	}
}

func TestIsCodeLine_LeavesProseAlone(t *testing.T) {
	for _, s := range []string{
		"Use React for the frontend",
		"The retry budget is 3 attempts (not 5)",
		"Tests went red→green after the fix",
		"Type checking runs before the build",
	} {
		if isCodeLine(s) {
			t.Errorf("prose %q read as code", s)
		}
	}
	if !strings.Contains(ContradictionDerivedBy(DetectorValue), ModelVersion) {
		t.Error("ContradictionDerivedBy must carry the model version")
	}
}
