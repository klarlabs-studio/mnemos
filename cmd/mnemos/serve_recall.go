package main

import (
	"net/http"
	"strconv"

	mnemos "go.klarlabs.de/mnemos"
)

// Advanced recall over HTTP (v0.68, parity batch 3). The basic hybrid retrieval
// is /v1/search; /v1/recall exposes the epistemic-honesty variants — recall that
// also reports whether the retrieved set is SUFFICIENT, spends a stakes-scaled
// effort budget, biases by a context string, surfaces CONFLICTS, or iterates
// retrieve<->reason rounds. A read (no side effects) → GET, so it needs no token.
//
//	GET /v1/recall?query=&mode=&limit=&hops=&run_id=&stakes=&context=&max_rounds=
//	mode: sufficiency (default) | effort | context | conflicts | iterative

type recallResultDTO struct {
	ClaimID     string  `json:"claim_id"`
	Text        string  `json:"text"`
	Type        string  `json:"type"`
	Confidence  float64 `json:"confidence"`
	TrustScore  float64 `json:"trust_score"`
	HopDistance int     `json:"hop_distance"`
	Provenance  string  `json:"provenance,omitempty"`
}

type sufficiencyDTO struct {
	Confidence float64 `json:"confidence"`
	ClaimCount int     `json:"claim_count"`
	Sufficient bool    `json:"sufficient"`
	Floor      float64 `json:"floor"`
}

type effortDTO struct {
	Budget  float64 `json:"budget"`
	Passes  int     `json:"passes"`
	Hops    int     `json:"hops"`
	Breadth int     `json:"breadth"`
}

type conflictDTO struct {
	ClaimID            string  `json:"claim_id"`
	ClaimText          string  `json:"claim_text"`
	ContradictingID    string  `json:"contradicting_id"`
	ContradictingText  string  `json:"contradicting_text"`
	ContradictingTrust float64 `json:"contradicting_trust"`
}

type recallResponseDTO struct {
	Mode        string            `json:"mode"`
	Results     []recallResultDTO `json:"results"`
	Sufficiency *sufficiencyDTO   `json:"sufficiency,omitempty"`
	Effort      *effortDTO        `json:"effort,omitempty"`
	Conflicts   []conflictDTO     `json:"conflicts,omitempty"`
	Rounds      int               `json:"rounds,omitempty"`
}

func resultsToDTO(rs []mnemos.Result) []recallResultDTO {
	out := make([]recallResultDTO, 0, len(rs))
	for _, r := range rs {
		out = append(out, recallResultDTO{r.ClaimID, r.Text, r.Type, r.Confidence, r.TrustScore, r.HopDistance, r.Provenance})
	}
	return out
}

func sufficiencyToDTO(s mnemos.Sufficiency) *sufficiencyDTO {
	return &sufficiencyDTO{s.Confidence, s.ClaimCount, s.Sufficient, s.Floor}
}

func makeRecallHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireGET(w, r) || memUnavailable(w, mem) {
			return
		}
		q := r.URL.Query()
		text := q.Get("query")
		if text == "" {
			writeError(w, http.StatusBadRequest, "query is required")
			return
		}
		query := mnemos.Query{Text: text, RunID: q.Get("run_id")}
		if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
			query.Limit = n
		}
		if n, err := strconv.Atoi(q.Get("hops")); err == nil && n > 0 {
			query.Hops = n
		}

		mode := q.Get("mode")
		if mode == "" {
			mode = "sufficiency"
		}
		resp := recallResponseDTO{Mode: mode}
		ctx := r.Context()

		switch mode {
		case "sufficiency":
			res, suf, err := mem.RecallWithSufficiency(ctx, query)
			if err != nil {
				writeInternalError(w, "recall", err)
				return
			}
			resp.Results, resp.Sufficiency = resultsToDTO(res), sufficiencyToDTO(suf)
		case "effort":
			stakes := 0.5
			if f, err := strconv.ParseFloat(q.Get("stakes"), 64); err == nil {
				stakes = f
			}
			res, suf, eff, err := mem.RecallWithEffort(ctx, query, stakes)
			if err != nil {
				writeInternalError(w, "recall", err)
				return
			}
			resp.Results, resp.Sufficiency = resultsToDTO(res), sufficiencyToDTO(suf)
			resp.Effort = &effortDTO{eff.Budget, eff.Passes, eff.Hops, eff.Breadth}
		case "context":
			res, err := mem.RecallWithContext(ctx, query, q.Get("context"))
			if err != nil {
				writeInternalError(w, "recall", err)
				return
			}
			resp.Results = resultsToDTO(res)
		case "conflicts":
			res, conflicts, err := mem.RecallWithConflicts(ctx, query)
			if err != nil {
				writeInternalError(w, "recall", err)
				return
			}
			resp.Results = resultsToDTO(res)
			for _, c := range conflicts {
				resp.Conflicts = append(resp.Conflicts, conflictDTO{c.ClaimID, c.ClaimText, c.ContradictingID, c.ContradictingText, c.ContradictingTrust})
			}
		case "iterative":
			maxRounds := 3
			if n, err := strconv.Atoi(q.Get("max_rounds")); err == nil && n > 0 {
				maxRounds = n
			}
			res, rounds, err := mem.RecallIterative(ctx, query, maxRounds)
			if err != nil {
				writeInternalError(w, "recall", err)
				return
			}
			resp.Results, resp.Rounds = resultsToDTO(res), rounds
		default:
			writeError(w, http.StatusBadRequest, "mode must be sufficiency, effort, context, conflicts, or iterative")
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}
