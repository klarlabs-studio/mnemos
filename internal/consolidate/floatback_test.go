package consolidate

import "testing"

func active(id, text, typ string, trust float64) FloatInput {
	return FloatInput{ID: id, Text: text, Type: typ, Active: true, Confidence: trust, Trust: trust}
}

// TestPlanFloatBack_GeneralityAndTrustGates is the core selection matrix: a
// high-trust general learning floats; a low-trust one and a repo-specific one do
// not.
func TestPlanFloatBack_GeneralityAndTrustGates(t *testing.T) {
	inputs := []FloatInput{
		active("c1", "always run migrations inside a transaction to keep them atomic", "fact", 0.9),
		active("c2", "the deploy might be faster if we cache modules", "hypothesis", 0.2),
		active("c3", "the retry limit in /Users/alice/app/internal/worker/pool.go should be 5", "decision", 0.95),
		active("c4", "bump the timeout in config.yaml to 30s", "fact", 0.95),
	}

	plan := PlanFloatBack(inputs, DefaultMinTrust, nil)

	if len(plan.Floated) != 1 || plan.Floated[0].ClaimID != "c1" {
		t.Fatalf("want only c1 floated, got %+v", plan.Floated)
	}
	if plan.Floated[0].Reason != ReasonFloatGeneral {
		t.Fatalf("c1 reason = %q, want %q", plan.Floated[0].Reason, ReasonFloatGeneral)
	}

	reasons := map[string]string{}
	for _, s := range plan.Skipped {
		reasons[s.ClaimID] = s.Reason
	}
	if reasons["c2"] != ReasonSkipBelowTrust {
		t.Errorf("c2 skip reason = %q, want below-trust", reasons["c2"])
	}
	if reasons["c3"] != ReasonSkipRepoLocal {
		t.Errorf("c3 (absolute path) skip reason = %q, want repo-local", reasons["c3"])
	}
	if reasons["c4"] != ReasonSkipRepoLocal {
		t.Errorf("c4 (filename) skip reason = %q, want repo-local", reasons["c4"])
	}
}

// TestPlanFloatBack_ExplicitGlobalFloatsUnconditionally proves the --global tag
// bypasses BOTH the trust and generality gates.
func TestPlanFloatBack_ExplicitGlobalFloatsUnconditionally(t *testing.T) {
	// Low trust AND repo-specific (a filename) — would be skipped twice over —
	// but tagged global.
	in := FloatInput{
		ID: "g1", Text: "remember: pin the base image in Dockerfile for repro builds",
		Type: "decision", Active: true, Confidence: 0.1, Trust: 0.1,
		ConfidenceComponents: map[string]float64{FloatGlobalComponent: 1},
	}
	plan := PlanFloatBack([]FloatInput{in}, DefaultMinTrust, nil)
	if len(plan.Floated) != 1 || plan.Floated[0].ClaimID != "g1" {
		t.Fatalf("global-tagged claim must float unconditionally, got floated=%+v skipped=%+v", plan.Floated, plan.Skipped)
	}
	if !plan.Floated[0].Global || plan.Floated[0].Reason != ReasonFloatExplicitGlobal {
		t.Fatalf("want explicit-global reason, got %+v", plan.Floated[0])
	}
}

// TestPlanFloatBack_DedupAgainstCentral proves a statement already present
// centrally (modulo punctuation/case/path) is skipped, so re-running is
// idempotent.
func TestPlanFloatBack_DedupAgainstCentral(t *testing.T) {
	text := "always run migrations inside a transaction"
	existing := map[string]struct{}{
		NormalizeForDedup(text): {},
	}
	// Same statement with different case/punctuation must still dedup.
	in := active("c1", "Always run migrations inside a transaction.", "fact", 0.9)
	plan := PlanFloatBack([]FloatInput{in}, DefaultMinTrust, existing)
	if len(plan.Floated) != 0 {
		t.Fatalf("duplicate must not float, got %+v", plan.Floated)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].Reason != ReasonSkipDuplicate {
		t.Fatalf("want dedup skip, got %+v", plan.Skipped)
	}
}

// TestPlanFloatBack_InactiveSkipped confirms only active claims float.
func TestPlanFloatBack_InactiveSkipped(t *testing.T) {
	in := FloatInput{ID: "d1", Text: "a general and true statement about systems", Type: "fact", Active: false, Trust: 0.99}
	plan := PlanFloatBack([]FloatInput{in}, DefaultMinTrust, nil)
	if len(plan.Skipped) != 1 || plan.Skipped[0].Reason != ReasonSkipInactive {
		t.Fatalf("want inactive skip, got floated=%+v skipped=%+v", plan.Floated, plan.Skipped)
	}
}

func TestStripRepoSpecifics(t *testing.T) {
	cases := map[string]string{
		"set the limit in /Users/alice/app/pool.go to 5": "set the limit in to 5",
		"edit ./cmd/main and ../lib/x then rebuild":      "edit and then rebuild",
		"see https://example.com/a/b for details":        "see https://example.com/a/b for details",
		"no paths here at all":                           "no paths here at all",
	}
	for in, want := range cases {
		if got := StripRepoSpecifics(in); got != want {
			t.Errorf("StripRepoSpecifics(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsRepoLocal(t *testing.T) {
	local := []string{
		"the fix lives in /var/lib/app/config",
		"update server.go to add the flag",
		"cherry-pick commit 1a2b3c4 onto main",
		"see ./scripts/deploy.sh",
	}
	for _, s := range local {
		if !IsRepoLocal(s) {
			t.Errorf("IsRepoLocal(%q) = false, want true", s)
		}
	}
	general := []string{
		"prefer idempotent operations for retryable work",
		"a rollback restores availability faster than a forward fix",
		"the ratio was 3 to 1 in favor of caching",
	}
	for _, s := range general {
		if IsRepoLocal(s) {
			t.Errorf("IsRepoLocal(%q) = true, want false", s)
		}
	}
}
