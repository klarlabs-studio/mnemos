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

// Time-bound results (#565): a single run's tally, and open/pending status. They
// are worth keeping for a while, but recalled months later as durable facts
// they are false or meaningless. Invented examples.
func TestHalfLifeFor_TimeBoundResults(t *testing.T) {
	cases := []struct {
		text string
		want float64
	}{
		{"In 31 of the 40 failed lookups, the cached entry had expired", VolatileHalfLifeDays},
		{"The cache covered 118 of 120 sessions on the rerun", VolatileHalfLifeDays},
		{"Of the 64 slow queries: 70% scanned the whole table", VolatileHalfLifeDays},
		{"412 claims (6.1%) got a resolved date", VolatileHalfLifeDays},
		{"The new ranker changes the top result on 12.5% of queries", VolatileHalfLifeDays},
		{"Issue #812 is still open", VolatileHalfLifeDays},
		{"The data import is still running in the background", VolatileHalfLifeDays},
		{"CI on #612 is waiting for maintainer approval", VolatileHalfLifeDays},
		{"The service is still running on Java 8 because of the vendor SDK", 0},
		{"Still open: the mergeability of the branch is unknown", VolatileHalfLifeDays},
		{"I merged #345 first, which closed #339", VolatileHalfLifeDays},
		{"I've reviewed #347 and it is good to merge", VolatileHalfLifeDays},

		// Durable quantities and history keep the default.
		{"Revenue grew 15% in Q3", 0},
		{"The retry budget is 3 attempts, not 5", 0},
		{"One in three requests is retried because the pool is undersized", 0},
		{"Issue templates live in .github/ISSUE_TEMPLATE", 0},
		{"I merged the feature flag code into the core package to simplify wiring", 0},
	}
	for _, tc := range cases {
		if got := HalfLifeFor("fact", tc.text); got != tc.want {
			t.Errorf("HalfLifeFor(%q) = %v, want %v", tc.text, got, tc.want)
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

// Typography must not defeat classification. These are the exact stored forms
// from a real brain that went unclassified — and then recalled at 90-day
// freshness while false — purely because of a curly apostrophe or backticks.
func TestHalfLifeFor_TypographyRobust(t *testing.T) {
	cases := []string{
		"Mnemos doesn’t work",                        // U+2019 right single quote
		"the build doesn’t compile",                  // same
		"No `warden` anywhere in the repo installed", // backtick-wrapped identifier
		"`postgres` is running on port 5432",         // backticks + state
	}
	for _, s := range cases {
		if got := HalfLifeFor("fact", s); got != VolatileHalfLifeDays {
			t.Errorf("typography defeated classification: HalfLifeFor(%q) = %v, want %v", s, got, VolatileHalfLifeDays)
		}
	}
	// The ASCII form and the curly form must agree.
	if HalfLifeFor("fact", "it doesn't work") != HalfLifeFor("fact", "it doesn’t work") {
		t.Error("ASCII and curly apostrophe classified differently")
	}
}

// Normalization must not turn a durable claim volatile. A backtick-quoted
// identifier inside a decision is still a decision.
func TestHalfLifeFor_NormalizationKeepsDurableDurable(t *testing.T) {
	durable := []string{
		"we chose `postgres` because the write volume outgrew `sqlite`",
		"the retry budget is 3 attempts by design",
	}
	for _, s := range durable {
		if got := HalfLifeFor("fact", s); got != 0 {
			t.Errorf("normalization decayed a durable claim: HalfLifeFor(%q) = %v, want 0", s, got)
		}
	}
}
