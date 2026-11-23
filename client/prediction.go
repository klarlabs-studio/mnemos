package client

import (
	"context"
	"net/http"
	"net/url"
)

// PredictiveErrorLevel is the prediction error at one level of the memory
// hierarchy. Samples 0 means the level has no signal and is left out of Total.
type PredictiveErrorLevel struct {
	Level   string  `json:"level"`
	Error   float64 `json:"error"`
	Samples int     `json:"samples"`
	Basis   string  `json:"basis"`
}

// PredictiveError is the hierarchical prediction-error report (ADR 0017).
// LevelsMeasured 0 means Total is unmeasured, not perfect.
type PredictiveError struct {
	Levels         []PredictiveErrorLevel `json:"levels"`
	Total          float64                `json:"total"`
	Hotspot        string                 `json:"hotspot"`
	LevelsMeasured int                    `json:"levels_measured"`
	// Attributed is the outcome surprise attributed to each belief a decision
	// rested on (ADR 0049), largest first.
	Attributed []AttributedSurprise `json:"attributed,omitempty"`
}

// AttributedSurprise is the outcome prediction error attributed to one belief.
type AttributedSurprise struct {
	ClaimID     string  `json:"claim_id"`
	Surprise    float64 `json:"surprise"`
	Predictions int     `json:"predictions"`
	Refuted     int     `json:"refuted"`
}

// PredictiveError reads where the brain's model is most wrong.
// GET /v1/predictive-error.
func (c *Client) PredictiveError(ctx context.Context) (*PredictiveError, error) {
	var out PredictiveError
	if err := c.do(ctx, http.MethodGet, "/v1/predictive-error", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Reconciliation is one prediction closed against its observed value.
// Confirmed is a validates verdict; false is a refutation.
type Reconciliation struct {
	ClaimID   string  `json:"belief_id"`
	Predicted float64 `json:"predicted"`
	Observed  float64 `json:"observed"`
	Surprise  float64 `json:"surprise"`
	Confirmed bool    `json:"confirmed"`
	// DecisionID is set for a decision-tied expectation (ADR 0049); ClaimID
	// is then the expectation's store key.
	DecisionID string `json:"decision_id,omitempty"`
	// AttributedTo are the beliefs the decision rested on.
	AttributedTo []string `json:"attributed_to,omitempty"`
	// Revisions are what the surprise did to those beliefs.
	Revisions []BeliefRevision `json:"revisions,omitempty"`
	// Rechecked marks an earlier surprise revised again in this pass.
	Rechecked bool `json:"rechecked,omitempty"`
}

// BeliefRevision is one surprise-triggered revision: Kind is "flag",
// "supersede" or "clear"; By is the superseding belief.
type BeliefRevision struct {
	ClaimID string `json:"claim_id"`
	Kind    string `json:"kind"`
	By      string `json:"by,omitempty"`
}

// ReconcileExpectations closes every observed-but-open expectation into a
// verdict and returns them. POST /v1/expectations/reconcile (claims:write).
func (c *Client) ReconcileExpectations(ctx context.Context) ([]Reconciliation, error) {
	var out struct {
		Reconciliations []Reconciliation `json:"reconciliations"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/expectations/reconcile", struct{}{}, &out); err != nil {
		return nil, err
	}
	return out.Reconciliations, nil
}

// RecordDecisionRequest is a decision to record. The server assigns the id.
// RiskLevel is low, medium, high or critical; ChosenAt is RFC 3339 or
// YYYY-MM-DD and defaults to now.
type RecordDecisionRequest struct {
	Statement    string   `json:"statement"`
	Plan         string   `json:"plan,omitempty"`
	Reasoning    string   `json:"reasoning,omitempty"`
	RiskLevel    string   `json:"risk_level"`
	Beliefs      []string `json:"beliefs,omitempty"`
	Alternatives []string `json:"alternatives,omitempty"`
	OutcomeID    string   `json:"outcome_id,omitempty"`
	ChosenAt     string   `json:"chosen_at,omitempty"`
	// Expectation is the outcome the decision is expected to produce,
	// resting on Beliefs (ADR 0049). Optional.
	Expectation *ExpectedOutcome `json:"expectation,omitempty"`
}

// ExpectedOutcome is a decision's expected outcome. Horizon is RFC 3339 or
// YYYY-MM-DD; empty means none.
type ExpectedOutcome struct {
	Predicted float64 `json:"predicted"`
	Tolerance float64 `json:"tolerance,omitempty"`
	Horizon   string  `json:"horizon,omitempty"`
}

// DecisionExpectation is a decision's expectation as stored.
type DecisionExpectation struct {
	DecisionID string `json:"decision_id"`
	Expectation
}

// RecordDecisionObservation records the observed outcome of a decision
// recorded with an expectation. POST /v1/decisions/{id}/observation
// (claims:write).
func (c *Client) RecordDecisionObservation(ctx context.Context, decisionID string, observed float64) (*DecisionExpectation, error) {
	var out DecisionExpectation
	if err := c.do(ctx, http.MethodPost, "/v1/decisions/"+url.PathEscape(decisionID)+"/observation", map[string]any{"observed": observed}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RecordDecision records an agent decision and returns it as stored, id
// included. POST /v1/decisions (claims:write).
func (c *Client) RecordDecision(ctx context.Context, req RecordDecisionRequest) (*Decision, error) {
	var out Decision
	if err := c.do(ctx, http.MethodPost, "/v1/decisions", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
