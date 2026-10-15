package extract

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Every input here was stored as a claim in a real 111k-claim brain, most of
// them many times over: source lines lifted from code the agent was reading or
// writing, and narration fragments that announce work without asserting
// anything.
var codeLineFixtures = []string{
	"type CompilationMachine interface {",
	"ExpiresAt     time.Time",
	"pub signing_key: String,",
	"// caller asks what is currently deployed",
	"/* retry budget */",
	"if err != nil {",
	"return nil, fmt.Errorf(\"open: %w\", err)",
	"x := buildIndex(claims)",
	"}",
	"});",
	"Name string `json:\"name\"`",
	"fn verify(token: &str) -> Result<Claims> {",
}

var narrationFragmentFixtures = []string{
	"Now the implementation",
	"Now step 2: tag the action",
	"Next step 3",
	"Pushing the release",
	"Running the tests",
	"Good so what's the remaining gap",
	"Ok so now the parser",
	"What's the remaining gap",
	"Now the doctor output",
}

// Knowledge that shares surface features with the noise above — a type name,
// a gerund, a leading "now", a code identifier, a colon — and must survive.
var fragmentLookalikes = []string{
	"We decided to use Postgres for the event store",
	"The retry budget is 3 attempts, not 5",
	"time.Time values are stored as RFC3339 strings",
	"Caching improves recall latency on large brains",
	"Now that the index exists, recall is O(matched rows)",
	"Pushing to main requires the warden/gate status",
	"The CompilationMachine interface owns the compile lifecycle",
	"ExpiresAt is checked before every token refresh",
	"Use go test -race before pushing",
	"The signing_key field holds the Ed25519 private key",
	"Postgres enforces tenant isolation with row-level security",
	"Beliefs are read with GET /v1/beliefs/{id}",
	"What the user asked for was a release by Friday",
}

func TestIsCodeLine(t *testing.T) {
	for _, s := range codeLineFixtures {
		if !isCodeLine(s) {
			t.Errorf("isCodeLine(%q) = false, want true", s)
		}
	}
	for _, s := range fragmentLookalikes {
		if isCodeLine(s) {
			t.Errorf("isCodeLine(%q) = true, want false (knowledge)", s)
		}
	}
}

func TestIsNarrationFragment(t *testing.T) {
	for _, s := range narrationFragmentFixtures {
		if !isNarrationFragment(s) {
			t.Errorf("isNarrationFragment(%q) = false, want true", s)
		}
	}
	for _, s := range fragmentLookalikes {
		if isNarrationFragment(s) {
			t.Errorf("isNarrationFragment(%q) = true, want false (knowledge)", s)
		}
	}
}

// The LLM path filters through isJunkClaim; both kinds of noise must be junk
// there, and the lookalikes must not be.
func TestIsJunkClaim_CodeLinesAndNarrationFragments(t *testing.T) {
	for _, s := range append(append([]string{}, codeLineFixtures...), narrationFragmentFixtures...) {
		if !isJunkClaim(s) {
			t.Errorf("isJunkClaim(%q) = false, want true", s)
		}
	}
	for _, s := range fragmentLookalikes {
		if isJunkClaim(s) {
			t.Errorf("isJunkClaim(%q) = true, want false (knowledge)", s)
		}
	}
}

// The rule-based engine is what every capture falls back to when the LLM is
// absent or fails (#451), so it must reject the same noise rather than store
// each line as a fact.
func TestEngineExtract_RejectsCodeLinesAndNarrationFragments(t *testing.T) {
	e := NewEngine()
	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	var events []domain.Event
	for i, s := range append(append([]string{}, codeLineFixtures...), narrationFragmentFixtures...) {
		events = append(events, domain.Event{
			ID: "ev_noise_" + string(rune('a'+i)), Content: s, SchemaVersion: "v1",
			SourceInputID: "in_noise", Timestamp: at, IngestedAt: at,
		})
	}
	claims, _, err := e.Extract(context.Background(), events)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, c := range claims {
		t.Errorf("rule engine stored noise as a claim: %q", c.Text)
	}
}

func TestEngineExtract_KeepsFragmentLookalikes(t *testing.T) {
	e := NewEngine()
	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	kept := map[string]bool{}
	for i, s := range fragmentLookalikes {
		claims, _, err := e.Extract(context.Background(), []domain.Event{{
			ID: "ev_keep_" + string(rune('a'+i)), Content: s, SchemaVersion: "v1",
			SourceInputID: "in_keep", Timestamp: at, IngestedAt: at,
		}})
		if err != nil {
			t.Fatalf("Extract(%q): %v", s, err)
		}
		for _, c := range claims {
			kept[c.Text] = true
		}
	}
	for _, s := range fragmentLookalikes {
		if !kept[s] {
			t.Errorf("rule engine dropped knowledge %q", s)
		}
	}
}
