package extract

import "testing"

// The stale beliefs that motivated this: each was true when written, and each
// kept recalling at full confidence after the system moved.
func TestHalfLifeFor_VolatileSystemState(t *testing.T) {
	volatile := []string{
		"warden is not installed anywhere in the repo",
		"no warden anywhere in the codebase",
		"briefkasten is running as an HTTP MCP server",
		"the mnemos MCP server is not wired up",
		"mnemos doesn't work",
		"the build is failing",
		"CI is green",
		"the service is currently on v0.18.0",
		"postgres is listening on port 5432",
	}
	for _, s := range volatile {
		if got := HalfLifeFor("fact", s); got != VolatileHalfLifeDays {
			t.Errorf("HalfLifeFor(%q) = %v, want %v (volatile system state)", s, got, VolatileHalfLifeDays)
		}
	}
}

// The failure that matters: mis-classifying durable knowledge as volatile
// decays it out of recall, and nobody sees it happen. These must keep the
// store default.
func TestHalfLifeFor_DurableKnowledge(t *testing.T) {
	durable := []string{
		"we chose Postgres because the write volume outgrew SQLite",
		"the retry budget is 3 attempts, not 5",
		"claims require at least one evidence event by design",
		"contradiction detection must never auto-deprecate, because the detectors produce false positives",
		"the team prefers small pull requests",
		"events are the immutable source of truth; claims are derived",
		"the trade-off is latency against consistency",
	}
	for _, s := range durable {
		if got := HalfLifeFor("fact", s); got != 0 {
			t.Errorf("HalfLifeFor(%q) = %v, want 0 (durable knowledge must not decay early)", s, got)
		}
	}
}

// A decision's value is its reasoning, which survives the thing it decided
// about changing — so decisions are never volatile regardless of wording.
func TestHalfLifeFor_DecisionsNeverVolatile(t *testing.T) {
	if got := HalfLifeFor("decision", "the service is running on v1.2.3"); got != 0 {
		t.Errorf("decision classified volatile (%v); a decision's reasoning does not expire", got)
	}
}

// A state verb inside a rationale is still a rationale.
func TestHalfLifeFor_DurableVetoesVolatile(t *testing.T) {
	s := "we chose libsql because the sqlite build is failing on windows"
	if got := HalfLifeFor("fact", s); got != 0 {
		t.Errorf("HalfLifeFor(%q) = %v, want 0 — the durable signal must veto", s, got)
	}
}

// Coverage is deliberately partial. The lexicon cannot enumerate every way
// English states system state, and the design chooses misses over false
// positives: a missed volatile claim leaves today's behaviour, while a
// mis-classified durable claim decays real knowledge out of recall invisibly.
//
// These are real examples the classifier does NOT catch. The assertion is that
// they fail SAFE — default half-life, not a wrong one. If a future change
// starts catching them, that is an improvement, and this test should be
// updated to say so rather than deleted.
func TestHalfLifeFor_KnownMissesFailSafe(t *testing.T) {
	misses := []string{
		"the migration lock is not released",
		"the feature flag was flipped on last week",
		"we are three commits behind upstream",
	}
	for _, s := range misses {
		if got := HalfLifeFor("fact", s); got != 0 && got != VolatileHalfLifeDays {
			t.Errorf("HalfLifeFor(%q) = %v — neither the default nor the volatile value", s, got)
		}
	}
}

func TestHalfLifeFor_EmptyAndUnknown(t *testing.T) {
	if got := HalfLifeFor("fact", ""); got != 0 {
		t.Errorf("empty text = %v, want 0", got)
	}
	// No confident signal either way: leave the default rather than guess.
	if got := HalfLifeFor("fact", "the customer had twelve prior refunds"); got != 0 {
		t.Errorf("unclassifiable claim = %v, want 0 (default)", got)
	}
}
