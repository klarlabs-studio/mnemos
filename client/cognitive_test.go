package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	client "go.klarlabs.de/mnemos/client"
)

// cognitiveStub serves canned connected-brain responses so the SDK's decoding +
// query wiring can be asserted without a real store.
func cognitiveStub(t *testing.T) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/who-knows":
			if r.URL.Query().Get("query") == "" {
				http.Error(w, `{"error":"query required"}`, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"experts": []client.Expert{
				{Worker: "grace", Affinity: 0.9, Reliability: 0.8, ClaimCount: 4},
			}})
		case r.URL.Path == "/v1/knowledge-gaps":
			_ = json.NewEncoder(w).Encode(map[string]any{"gaps": []client.Gap{
				{ClaimID: "cl1", Text: "unresolved", Kind: "GapUnresolvedHypothesis", Score: 0.5},
			}})
		case r.URL.Path == "/v1/calibration":
			_ = json.NewEncoder(w).Encode(client.Calibration{Samples: 12, ECE: 0.07, Brier: 0.2,
				Buckets: []client.CalibrationBucket{{Lower: 0.8, Upper: 0.9, Count: 5, MeanConfidence: 0.85, Accuracy: 0.8}},
				Sources: []client.CalibrationSource{{Source: "grace", Samples: 5, Gap: 0.05}}})
		case r.URL.Path == "/v1/hypercorrections":
			_ = json.NewEncoder(w).Encode(map[string]any{"hypercorrections": []client.Hypercorrection{
				{ContradictedClaimID: "cl_old", ChallengingClaimID: "cl_new", ContradictedPromoted: true},
			}})
		case r.URL.Path == "/v1/recombinations":
			_ = json.NewEncoder(w).Encode(map[string]any{"recombinations": []client.Recombination{
				{ClaimA: "a", ClaimB: "b", Similarity: 0.6},
			}})
		case strings.HasSuffix(r.URL.Path, "/analogous"):
			_ = json.NewEncoder(w).Encode(map[string]any{"analogous": []client.Analogy{
				{ClaimID: "cl2", Text: "similar", Similarity: 0.7},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL)
}

func TestCognitive_WhoKnows(t *testing.T) {
	experts, err := cognitiveStub(t).WhoKnows(context.Background(), "creatinine", 5)
	if err != nil {
		t.Fatalf("WhoKnows: %v", err)
	}
	if len(experts) != 1 || experts[0].Worker != "grace" || experts[0].ClaimCount != 4 {
		t.Fatalf("unexpected experts: %+v", experts)
	}
}

func TestCognitive_KnowledgeGaps(t *testing.T) {
	gaps, err := cognitiveStub(t).KnowledgeGaps(context.Background(), 10)
	if err != nil || len(gaps) != 1 || gaps[0].ClaimID != "cl1" {
		t.Fatalf("KnowledgeGaps: %v %+v", err, gaps)
	}
}

func TestCognitive_Calibration(t *testing.T) {
	cal, err := cognitiveStub(t).Calibration(context.Background())
	if err != nil || cal == nil || cal.Samples != 12 || len(cal.Buckets) != 1 || len(cal.Sources) != 1 {
		t.Fatalf("Calibration: %v %+v", err, cal)
	}
	if cal.Sources[0].Source != "grace" {
		t.Fatalf("source not decoded: %+v", cal.Sources)
	}
}

func TestCognitive_Hypercorrections(t *testing.T) {
	hcs, err := cognitiveStub(t).Hypercorrections(context.Background())
	if err != nil || len(hcs) != 1 || !hcs[0].ContradictedPromoted {
		t.Fatalf("Hypercorrections: %v %+v", err, hcs)
	}
}

func TestCognitive_Recombinations(t *testing.T) {
	recs, err := cognitiveStub(t).Recombinations(context.Background(), 10)
	if err != nil || len(recs) != 1 || recs[0].Similarity != 0.6 {
		t.Fatalf("Recombinations: %v %+v", err, recs)
	}
}

func TestCognitive_AnalogousClaims(t *testing.T) {
	an, err := cognitiveStub(t).AnalogousClaims(context.Background(), "cl1", 5)
	if err != nil || len(an) != 1 || an[0].ClaimID != "cl2" {
		t.Fatalf("AnalogousClaims: %v %+v", err, an)
	}
}
