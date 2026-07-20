package extract

import (
	"regexp"
	"strings"
)

// Operational-event classification (ADR 0023 part 2).
//
// An operational event is a past action on a system: a deploy, a release, a
// merge, an incident. Unlike status chatter ("the suite is green" — dropped as
// junk) it is genuine history worth keeping; unlike knowledge ("we chose
// Postgres because writes outgrew SQLite") its value is *when it happened*, not
// a durable truth. It belongs in the episodic/temporal layer (timeline_query),
// not the belief graph where it recalls at full confidence a month later and
// feeds pairwise contradiction detection.
//
// This classifier only IDENTIFIES event-observations. Routing them (keeping the
// belief out of the graph, surfacing the source event as a typed episode) is a
// separate pipeline change — see ADR 0023 — because it must run before
// relationship detection to avoid dangling edges and it spans the
// extract/persist seam.
//
// The precision bar is the highest of any filter in this line of work: a
// missed event stays a belief (status quo, harmless), but a durable claim
// wrongly classified as an event and dropped from beliefs is an INVISIBLE loss
// of real knowledge. So a match requires BOTH a past-action verb AND a concrete
// artifact (a version, a PR, a named service), and durable framing (a decision,
// a reason) vetoes it.

// EventKind labels the sort of operational event a claim records.
type EventKind string

// Event kinds. EventNone means the claim is not an operational event.
const (
	EventNone       EventKind = ""
	EventDeployment EventKind = "deployment"
	EventRelease    EventKind = "release"
	EventChange     EventKind = "change"   // merges, pushes, reverts
	EventIncident   EventKind = "incident" // outages, incidents
)

// deploymentVerbRE — a system was moved to an environment.
var deploymentVerbRE = regexp.MustCompile(`(?i)\b(?:deployed|rolled\s+out|rolled\s+back|promoted|provisioned|scaled(?:\s+up|\s+down)?)\b`)

// releaseVerbRE — a version was cut/published.
var releaseVerbRE = regexp.MustCompile(`(?i)\b(?:released|published|shipped|cut|tagged|bumped)\b`)

// changeVerbRE — a change landed.
var changeVerbRE = regexp.MustCompile(`(?i)\b(?:merged|pushed|reverted|reved|cherry-picked|squash-merged|force-pushed)\b`)

// incidentRE — an operational incident. Requires an incident noun; a bare
// "failed" is too common in durable claims.
var incidentRE = regexp.MustCompile(`(?i)\b(?:outage|incident|downtime|regression|breakage)\b`)

// incidentOccurredRE requires the incident to have HAPPENED, not merely be
// mentioned in a plan or piece of advice ("an incident plan", "during an
// outage"). Pairs with incidentRE.
var incidentOccurredRE = regexp.MustCompile(`(?i)\b(?:started|occurred|happened|hit|struck|took\s+down|brought\s+down|caused|broke|crashed|went\s+down|at\s+\d)\b`)

// artifactRE — a concrete thing the action acted on: a semver, a PR number, a
// tag, a container ref. This is the precision anchor: "deployed" alone could be
// a plan ("we plan to deploy Fridays"); "deployed v1.2.3" is an event.
var artifactRE = regexp.MustCompile(`(?i)(?:\bv?\d+\.\d+(?:\.\d+)?\b|#\d+\b|\bPR\s*#?\d+\b|\brelease\b|\bbuild\b|\bcommit\b|\b[0-9a-f]{7,40}\b)`)

// eventDurableVetoRE — framing that marks the sentence as a decision, plan, or
// rationale rather than a record of something that happened. A decision to
// deploy is durable knowledge; a deploy is an event.
var eventDurableVetoRE = regexp.MustCompile(`(?i)(?:\?|` +
	`\b(?:should|shall|must|will|would|could|might|want\s+me|happy\s+to|shall\s+i|whether|rather|instead|worth|plan(?:ned)?\s+to|going\s+to|about\s+to|because|in\s+order\s+to|so\s+that|decided|the\s+reason|policy|always|never|by\s+design|prefer|recommend|i'?d|we'?d|let'?s|next\s+release|until\s+a\s+release)\b)`)

// ClassifyEventObservation reports the operational-event kind a claim records,
// or EventNone. Decisions never qualify — a decision's value is its reasoning,
// which survives the event it decided about.
func ClassifyEventObservation(claimType, text string) EventKind {
	if strings.EqualFold(claimType, "decision") {
		return EventNone
	}
	t := normalizeForMatch(strings.TrimSpace(text))
	if t == "" {
		return EventNone
	}
	if eventDurableVetoRE.MatchString(t) {
		return EventNone
	}

	// Incidents: an incident noun AND an occurrence signal. The noun alone
	// matched plans and advice ("an incident plan", "worth rotating before an
	// outage"), which are not events.
	if incidentRE.MatchString(t) && incidentOccurredRE.MatchString(t) {
		return EventIncident
	}

	// Every other kind requires a concrete artifact, the precision anchor.
	if !artifactRE.MatchString(t) {
		return EventNone
	}
	switch {
	case deploymentVerbRE.MatchString(t):
		return EventDeployment
	case releaseVerbRE.MatchString(t):
		return EventRelease
	case changeVerbRE.MatchString(t):
		return EventChange
	}
	return EventNone
}

// IsEventObservation is the boolean form.
func IsEventObservation(claimType, text string) bool {
	return ClassifyEventObservation(claimType, text) != EventNone
}
