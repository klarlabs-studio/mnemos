package client

import (
	"context"
	"net/http"
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
