package main

import (
	"net/http"
	"strconv"
	"time"

	mnemos "go.klarlabs.de/mnemos"
)

// The cognitive endpoints expose the library's connected-brain reads
// (who-knows-what, knowledge gaps, calibration, hypercorrections,
// recombinations, analogical retrieval) over HTTP, so an out-of-process
// consumer gets the same brain a Go-embedding consumer does. Each handler
// delegates to the Memory facade `serve` builds over the same store; when the
// facade is unavailable (e.g. no DSN) they return 503 rather than 500.

func parseLimitQuery(r *http.Request, def int) int {
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// memUnavailable writes a 503 when the cognitive facade wasn't constructed.
func memUnavailable(w http.ResponseWriter, mem mnemos.Memory) bool {
	if mem == nil {
		writeError(w, http.StatusServiceUnavailable, "cognitive layer unavailable (no memory facade — check MNEMOS_DB_URL)")
		return true
	}
	return false
}

func requireGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	return true
}

// --- who-knows ---------------------------------------------------------------

type expertDTO struct {
	Worker      string  `json:"worker"`
	Affinity    float64 `json:"affinity"`
	Reliability float64 `json:"reliability"`
	ClaimCount  int     `json:"claim_count"`
}

func makeWhoKnowsHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireGET(w, r) || memUnavailable(w, mem) {
			return
		}
		query := r.URL.Query().Get("query")
		if query == "" {
			writeError(w, http.StatusBadRequest, "query is required")
			return
		}
		experts, err := mem.WhoKnows(r.Context(), query, parseLimitQuery(r, 10))
		if err != nil {
			writeInternalError(w, "who-knows", err)
			return
		}
		out := make([]expertDTO, 0, len(experts))
		for _, e := range experts {
			out = append(out, expertDTO{e.Worker, e.Affinity, e.Reliability, e.ClaimCount})
		}
		writeJSON(w, http.StatusOK, map[string]any{"query": query, "experts": out})
	}
}

// --- knowledge-gaps ----------------------------------------------------------

type gapDTO struct {
	ClaimID string  `json:"claim_id"`
	Text    string  `json:"text"`
	Kind    string  `json:"kind"`
	Score   float64 `json:"score"`
}

func makeKnowledgeGapsHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireGET(w, r) || memUnavailable(w, mem) {
			return
		}
		gaps, err := mem.KnowledgeGaps(r.Context(), parseLimitQuery(r, 20))
		if err != nil {
			writeInternalError(w, "knowledge-gaps", err)
			return
		}
		out := make([]gapDTO, 0, len(gaps))
		for _, g := range gaps {
			out = append(out, gapDTO{g.ClaimID, g.Text, g.Kind, g.Score})
		}
		writeJSON(w, http.StatusOK, map[string]any{"gaps": out})
	}
}

// --- calibration -------------------------------------------------------------

type calibrationBucketDTO struct {
	Lower          float64 `json:"lower"`
	Upper          float64 `json:"upper"`
	Count          int     `json:"count"`
	MeanConfidence float64 `json:"mean_confidence"`
	Accuracy       float64 `json:"accuracy"`
}

type calibrationSourceDTO struct {
	Source         string  `json:"source"`
	Samples        int     `json:"samples"`
	MeanConfidence float64 `json:"mean_confidence"`
	Accuracy       float64 `json:"accuracy"`
	Gap            float64 `json:"gap"`
	Brier          float64 `json:"brier"`
}

type calibrationDTO struct {
	Samples int                    `json:"samples"`
	ECE     float64                `json:"ece"`
	Brier   float64                `json:"brier"`
	Buckets []calibrationBucketDTO `json:"buckets"`
	Sources []calibrationSourceDTO `json:"sources"`
}

func makeCalibrationHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireGET(w, r) || memUnavailable(w, mem) {
			return
		}
		cal, err := mem.Calibration(r.Context())
		if err != nil {
			writeInternalError(w, "calibration", err)
			return
		}
		dto := calibrationDTO{Samples: cal.Samples, ECE: cal.ECE, Brier: cal.Brier}
		for _, b := range cal.Buckets {
			dto.Buckets = append(dto.Buckets, calibrationBucketDTO{b.Lower, b.Upper, b.Count, b.MeanConfidence, b.Accuracy})
		}
		for _, s := range cal.Sources {
			dto.Sources = append(dto.Sources, calibrationSourceDTO{s.Source, s.Samples, s.MeanConfidence, s.Accuracy, s.Gap, s.Brier})
		}
		writeJSON(w, http.StatusOK, dto)
	}
}

// --- hypercorrections --------------------------------------------------------

type hypercorrectionDTO struct {
	ContradictedClaimID  string  `json:"contradicted_claim_id"`
	ContradictedText     string  `json:"contradicted_text"`
	ContradictedTrust    float64 `json:"contradicted_trust"`
	ContradictedPromoted bool    `json:"contradicted_promoted"`
	ChallengingClaimID   string  `json:"challenging_claim_id"`
	ChallengingText      string  `json:"challenging_text"`
	DetectedAt           string  `json:"detected_at,omitempty"`
}

func makeHypercorrectionsHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireGET(w, r) || memUnavailable(w, mem) {
			return
		}
		hcs, err := mem.Hypercorrections(r.Context())
		if err != nil {
			writeInternalError(w, "hypercorrections", err)
			return
		}
		out := make([]hypercorrectionDTO, 0, len(hcs))
		for _, h := range hcs {
			d := hypercorrectionDTO{
				ContradictedClaimID:  h.ContradictedClaimID,
				ContradictedText:     h.ContradictedText,
				ContradictedTrust:    h.ContradictedTrust,
				ContradictedPromoted: h.ContradictedPromoted,
				ChallengingClaimID:   h.ChallengingClaimID,
				ChallengingText:      h.ChallengingText,
			}
			if !h.DetectedAt.IsZero() {
				d.DetectedAt = h.DetectedAt.UTC().Format(time.RFC3339)
			}
			out = append(out, d)
		}
		writeJSON(w, http.StatusOK, map[string]any{"hypercorrections": out})
	}
}

// --- recombinations ----------------------------------------------------------

type recombinationDTO struct {
	ClaimA     string  `json:"claim_a"`
	TextA      string  `json:"text_a"`
	ClaimB     string  `json:"claim_b"`
	TextB      string  `json:"text_b"`
	Similarity float64 `json:"similarity"`
}

func makeRecombinationsHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireGET(w, r) || memUnavailable(w, mem) {
			return
		}
		recs, err := mem.Recombinations(r.Context(), parseLimitQuery(r, 20))
		if err != nil {
			writeInternalError(w, "recombinations", err)
			return
		}
		out := make([]recombinationDTO, 0, len(recs))
		for _, rc := range recs {
			out = append(out, recombinationDTO{rc.ClaimA, rc.TextA, rc.ClaimB, rc.TextB, rc.Similarity})
		}
		writeJSON(w, http.StatusOK, map[string]any{"recombinations": out})
	}
}

// --- analogous (claim subresource) -------------------------------------------

type analogyDTO struct {
	ClaimID    string  `json:"claim_id"`
	Text       string  `json:"text"`
	Similarity float64 `json:"similarity"`
}

// makeAnalogousHandler serves GET /v1/claims/<id>/analogous — the claims most
// structurally analogous to the given one (Weisfeiler-Lehman similarity).
func makeAnalogousHandler(mem mnemos.Memory, claimID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireGET(w, r) || memUnavailable(w, mem) {
			return
		}
		analogies, err := mem.AnalogousClaims(r.Context(), claimID, parseLimitQuery(r, 10))
		if err != nil {
			writeInternalError(w, "analogous", err)
			return
		}
		out := make([]analogyDTO, 0, len(analogies))
		for _, a := range analogies {
			out = append(out, analogyDTO{a.ClaimID, a.Text, a.Similarity})
		}
		writeJSON(w, http.StatusOK, map[string]any{"claim_id": claimID, "analogous": out})
	}
}
