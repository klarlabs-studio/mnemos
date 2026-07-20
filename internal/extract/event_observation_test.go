package extract

import "testing"

func TestClassifyEventObservation(t *testing.T) {
	events := map[string]EventKind{
		"deployed v1.2.3 to production at 14:00":  EventDeployment,
		"released v0.104.0 with 15 signed assets": EventRelease,
		"shipped the release, all assets signed":  EventRelease,
		"merged PR #223":                          EventChange,
		"squash-merged #220, branch deleted":      EventChange,
		"the outage started at 09:15":             EventIncident,
		"a regression broke the login flow":       EventIncident,
		"cut v0.19.1 covering the three fixes":    EventRelease,
	}
	for text, want := range events {
		if got := ClassifyEventObservation("fact", text); got != want {
			t.Errorf("ClassifyEventObservation(%q) = %q, want %q", text, got, want)
		}
	}
}

// The critical direction: durable knowledge must NEVER be classified as an
// event, because routing it out of the belief graph is an invisible loss.
func TestClassifyEventObservation_KeepsDurableKnowledge(t *testing.T) {
	durable := []string{
		"we chose to deploy on Fridays because traffic is lowest", // decision
		"we should release more often",                            // plan/opinion
		"the deploy process must never skip the migration step",   // policy
		"we plan to deploy v2 next quarter",                       // plan
		"deploying takes about six minutes",                       // general fact, no artifact
		"the release cadence is monthly by design",                // policy
		"merging is blocked until CI is green",                    // rule, no artifact
		"the payments service handles 3000 requests per second",   // durable fact
	}
	for _, s := range durable {
		if got := ClassifyEventObservation("fact", s); got != EventNone {
			t.Errorf("classified durable knowledge as event %q: %q", got, s)
		}
	}
}

// A decision that mentions a deploy is still a decision.
// The precision bar requires a concrete artifact, so genuine events phrased
// without one are deliberate MISSES — a safe miss (stays a belief), never a
// wrong classification. Documented so a future change that starts catching
// them is a deliberate choice, not an accident.
func TestClassifyEventObservation_ArtifactlessEventsAreSafeMisses(t *testing.T) {
	misses := []string{
		"rolled back the payments deploy",
		"deployed the new caching layer",
		"we merged the feature branch",
	}
	for _, s := range misses {
		if got := ClassifyEventObservation("fact", s); got != EventNone {
			t.Errorf("artifactless event should be a safe miss, got %q for %q", got, s)
		}
	}
}

func TestClassifyEventObservation_DecisionsNeverEvents(t *testing.T) {
	if got := ClassifyEventObservation("decision", "deployed v1.2.3 after the incident review"); got != EventNone {
		t.Errorf("decision classified as event %q", got)
	}
}
