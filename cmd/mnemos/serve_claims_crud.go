package main

import (
	"net/http"
	"strings"
	"time"

	mnemos "go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/domain"
)

// Claim CRUD parity (v0.67): single-claim Get, lifecycle transitions, novelty
// classification, and the decision browse surface — the library reads/writes
// an out-of-process consumer previously couldn't reach. All delegate to the
// Memory facade; 503 when it's unavailable.

// notFound reports whether an error is a facade "not found" (vs a real fault),
// so a missing id maps to 404 rather than 500. The library has no sentinel, so
// this matches on the message the Get/GetDecision paths produce.
func notFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not found")
}

// --- GET /v1/beliefs/{id} -----------------------------------------------------

type fullClaimDTO struct {
	ID         string  `json:"id"`
	Statement  string  `json:"statement"`
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence"`
	TrustScore float64 `json:"trust_score"`
	Lifecycle  string  `json:"lifecycle,omitempty"`
	ValidFrom  string  `json:"valid_from,omitempty"`
	ValidUntil string  `json:"valid_until,omitempty"`
	RecordedAt string  `json:"recorded_at,omitempty"`
}

func makeGetClaimHandler(mem mnemos.Memory, claimID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mem := scopedMem(r.Context(), mem)
		if !requireGET(w, r) || memUnavailable(w, mem) {
			return
		}
		c, err := mem.Get(r.Context(), claimID)
		if err != nil {
			if notFound(err) {
				writeError(w, http.StatusNotFound, "claim not found")
				return
			}
			writeInternalError(w, "get claim", err)
			return
		}
		dto := fullClaimDTO{
			ID: c.ID, Statement: c.Statement, Type: c.Type,
			Confidence: c.Confidence, TrustScore: c.TrustScore, Lifecycle: string(c.Lifecycle),
		}
		if !c.ValidFrom.IsZero() {
			dto.ValidFrom = c.ValidFrom.UTC().Format(time.RFC3339)
		}
		if c.ValidUntil != nil && !c.ValidUntil.IsZero() {
			dto.ValidUntil = c.ValidUntil.UTC().Format(time.RFC3339)
		}
		if !c.RecordedAt.IsZero() {
			dto.RecordedAt = c.RecordedAt.UTC().Format(time.RFC3339)
		}
		writeJSON(w, http.StatusOK, dto)
	}
}

// --- POST /v1/beliefs/{id}/lifecycle ------------------------------------------

func makeLifecycleHandler(mem mnemos.Memory, claimID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mem := scopedMem(r.Context(), mem)
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if memUnavailable(w, mem) || !requireScope(w, r, domain.ScopeClaimsWrite) {
			return
		}
		var req struct {
			Lifecycle string `json:"lifecycle"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
			return
		}
		lc := mnemos.ClaimLifecycle(req.Lifecycle)
		switch lc {
		case mnemos.ClaimLifecycleCandidate, mnemos.ClaimLifecyclePromoted, mnemos.ClaimLifecycleSuperseded:
		default:
			writeError(w, http.StatusBadRequest, "lifecycle must be candidate, promoted, or superseded")
			return
		}
		if err := mem.SetClaimLifecycle(r.Context(), claimID, lc); err != nil {
			if notFound(err) {
				writeError(w, http.StatusNotFound, "claim not found")
				return
			}
			writeInternalError(w, "set claim lifecycle", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"claim_id": claimID, "lifecycle": req.Lifecycle})
	}
}

// --- POST /v1/classify -------------------------------------------------------

type assimilationDTO struct {
	Kind       string  `json:"kind"`
	AnchorID   string  `json:"anchor_id,omitempty"`
	AnchorText string  `json:"anchor_text,omitempty"`
	Confidence float64 `json:"confidence"`
}

func makeClassifyHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mem := scopedMem(r.Context(), mem)
		// A read (novelty check, no side effects) → GET, so it needs no token.
		if !requireGET(w, r) || memUnavailable(w, mem) {
			return
		}
		text := r.URL.Query().Get("text")
		if strings.TrimSpace(text) == "" {
			writeError(w, http.StatusBadRequest, "text is required")
			return
		}
		a, err := mem.ClassifyClaim(r.Context(), text)
		if err != nil {
			writeInternalError(w, "classify", err)
			return
		}
		writeJSON(w, http.StatusOK, assimilationDTO{a.Kind, a.AnchorID, a.AnchorText, a.Confidence})
	}
}

// --- decisions ---------------------------------------------------------------

type decisionDTO struct {
	ID           string   `json:"id"`
	Statement    string   `json:"statement"`
	Plan         string   `json:"plan,omitempty"`
	Reasoning    string   `json:"reasoning,omitempty"`
	RiskLevel    string   `json:"risk_level,omitempty"`
	Beliefs      []string `json:"beliefs,omitempty"`
	Alternatives []string `json:"alternatives,omitempty"`
	OutcomeID    string   `json:"outcome_id,omitempty"`
	ChosenAt     string   `json:"chosen_at,omitempty"`
}

func decisionToDTO(d mnemos.Decision) decisionDTO {
	dto := decisionDTO{
		ID: d.ID, Statement: d.Statement, Plan: d.Plan, Reasoning: d.Reasoning,
		RiskLevel: d.RiskLevel, Beliefs: d.Beliefs, Alternatives: d.Alternatives, OutcomeID: d.OutcomeID,
	}
	if !d.ChosenAt.IsZero() {
		dto.ChosenAt = d.ChosenAt.UTC().Format(time.RFC3339)
	}
	return dto
}

// makeDecisionsHandler serves GET /v1/decisions?limit= (the decision browse list).
func makeDecisionsHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mem := scopedMem(r.Context(), mem)
		if !requireGET(w, r) || memUnavailable(w, mem) {
			return
		}
		decisions, err := mem.ListDecisions(r.Context(), parseLimitQuery(r, 50))
		if err != nil {
			writeInternalError(w, "list decisions", err)
			return
		}
		out := make([]decisionDTO, 0, len(decisions))
		for _, d := range decisions {
			out = append(out, decisionToDTO(d))
		}
		writeJSON(w, http.StatusOK, map[string]any{"decisions": out})
	}
}

// makeDecisionSubresourceHandler serves GET /v1/decisions/{id}.
func makeDecisionSubresourceHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mem := scopedMem(r.Context(), mem)
		if !requireGET(w, r) || memUnavailable(w, mem) {
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/decisions/")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		d, err := mem.GetDecision(r.Context(), id)
		if err != nil {
			if notFound(err) {
				writeError(w, http.StatusNotFound, "decision not found")
				return
			}
			writeInternalError(w, "get decision", err)
			return
		}
		writeJSON(w, http.StatusOK, decisionToDTO(d))
	}
}
