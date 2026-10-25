package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ActionKind classifies what an Action represents. The set is
// intentionally short and operations-oriented; downstream synthesis
// (lessons, playbooks) clusters by Kind so a longer ontology trades
// signal for noise.
type ActionKind string

// Supported ActionKind values. Backends store the underlying string,
// so adding a new kind here is non-breaking.
const (
	ActionKindDeploy    ActionKind = "deploy"
	ActionKindRollback  ActionKind = "rollback"
	ActionKindRestart   ActionKind = "restart"
	ActionKindScale     ActionKind = "scale"
	ActionKindConfigure ActionKind = "configure"
	ActionKindMigrate   ActionKind = "migrate"
	ActionKindFlag      ActionKind = "feature_flag"
	ActionKindHotfix    ActionKind = "hotfix"
	ActionKindCustom    ActionKind = "custom"
)

// ActionMetadataEnv and ActionMetadataTeam are the Action.Metadata keys that
// name the environment and team an action ran in. Synthesis clusters actions
// within (subject, env, team), so a lesson learned in staging never covers
// prod. Actions without them cluster as before, with an empty env or team.
const (
	ActionMetadataEnv  = "env"
	ActionMetadataTeam = "team"
)

// Action is a recorded operational change. Actions are paired with
// Outcomes (one Action → 0..N Outcomes) so the synthesis layer can
// cluster action→outcome chains into Lessons and Playbooks.
//
// Actions are append-only: an action's facts (subject, kind, time)
// reflect the world as recorded. Status drift is captured by emitting
// a follow-up Action plus the corresponding Outcome rather than by
// mutating an existing row.
type Action struct {
	ID        string
	RunID     string
	Kind      ActionKind
	Subject   string // service name, component id, or other operational target
	Actor     string // user id or agent id that performed the action
	At        time.Time
	Metadata  map[string]string
	CreatedBy string
	CreatedAt time.Time
}

// Validate enforces the minimum fields required for persistence.
// Metadata is allowed to be nil; the storage layer normalises to an
// empty JSON object.
func (a Action) Validate() error {
	if strings.TrimSpace(a.ID) == "" {
		return errors.New("action id is required")
	}
	if strings.TrimSpace(string(a.Kind)) == "" {
		return errors.New("action kind is required")
	}
	if strings.TrimSpace(a.Subject) == "" {
		return errors.New("action subject is required")
	}
	if a.At.IsZero() {
		return errors.New("action at is required")
	}
	return nil
}

// OutcomeResult labels an outcome's verdict in coarse terms. The
// numeric details live in Metrics; Result exists so the synthesis
// layer can cluster on success vs failure without re-deriving from
// metric values.
type OutcomeResult string

// Supported OutcomeResult values.
const (
	OutcomeResultSuccess OutcomeResult = "success"
	OutcomeResultFailure OutcomeResult = "failure"
	OutcomeResultPartial OutcomeResult = "partial"
	OutcomeResultUnknown OutcomeResult = "unknown"
)

// Outcome is the observed result of an Action. ActionID points back to
// the originating Action so the graph can answer "what did the deploy
// actually produce?". Metrics carries arbitrary numeric observations
// (latency_before, latency_after, error_rate, ...). Mnemos doesn't
// interpret metric keys — the synthesis layer compares like-named
// keys across outcomes when clustering.
type Outcome struct {
	ID         string
	ActionID   string
	Result     OutcomeResult
	Metrics    map[string]float64
	Notes      string
	ObservedAt time.Time
	Source     string // "push" (agent/CLI report) or "pull:<adapter>" (e.g. pull:prometheus)
	CreatedBy  string
	CreatedAt  time.Time
}

// Validate enforces the minimum fields required for persistence.
func (o Outcome) Validate() error {
	if strings.TrimSpace(o.ID) == "" {
		return errors.New("outcome id is required")
	}
	if strings.TrimSpace(o.ActionID) == "" {
		return errors.New("outcome action_id is required")
	}
	if strings.TrimSpace(string(o.Result)) == "" {
		return errors.New("outcome result is required")
	}
	switch o.Result {
	case OutcomeResultSuccess, OutcomeResultFailure, OutcomeResultPartial, OutcomeResultUnknown:
	default:
		return fmt.Errorf("invalid outcome result %q", o.Result)
	}
	if o.ObservedAt.IsZero() {
		return errors.New("outcome observed_at is required")
	}
	return nil
}
