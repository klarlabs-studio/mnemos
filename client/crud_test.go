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

func crudStub(t *testing.T, gotLifecycle *string) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/claims/cl1" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(client.ClaimDetail{ID: "cl1", Statement: "x", Type: "fact", TrustScore: 0.7, Lifecycle: "promoted"})
		case r.URL.Path == "/v1/claims/missing" && r.Method == http.MethodGet:
			http.Error(w, `{"error":"claim not found"}`, http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/lifecycle") && r.Method == http.MethodPost:
			var b struct {
				Lifecycle string `json:"lifecycle"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			if gotLifecycle != nil {
				*gotLifecycle = b.Lifecycle
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"claim_id": "cl1", "lifecycle": b.Lifecycle})
		case r.URL.Path == "/v1/classify" && r.Method == http.MethodGet:
			if r.URL.Query().Get("text") == "" {
				http.Error(w, `{"error":"text required"}`, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(client.Assimilation{Kind: "AssimilationNovel", Confidence: 0.2})
		case r.URL.Path == "/v1/decisions" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"decisions": []client.Decision{{ID: "d1", Statement: "ship it", RiskLevel: "low"}}})
		case r.URL.Path == "/v1/decisions/d1" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(client.Decision{ID: "d1", Statement: "ship it"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL)
}

func TestCRUD_GetClaim(t *testing.T) {
	c := crudStub(t, nil)
	got, err := c.GetClaim(context.Background(), "cl1")
	if err != nil || got == nil || got.ID != "cl1" || got.Lifecycle != "promoted" {
		t.Fatalf("GetClaim: %v %+v", err, got)
	}
	missing, err := c.GetClaim(context.Background(), "missing")
	if err != nil || missing != nil {
		t.Fatalf("GetClaim missing should be nil,nil: %v %+v", err, missing)
	}
}

func TestCRUD_SetClaimLifecycle(t *testing.T) {
	var got string
	c := crudStub(t, &got)
	if err := c.SetClaimLifecycle(context.Background(), "cl1", "superseded"); err != nil {
		t.Fatalf("SetClaimLifecycle: %v", err)
	}
	if got != "superseded" {
		t.Fatalf("lifecycle not sent: %q", got)
	}
}

func TestCRUD_Classify(t *testing.T) {
	a, err := crudStub(t, nil).Classify(context.Background(), "new statement")
	if err != nil || a == nil || a.Kind != "AssimilationNovel" {
		t.Fatalf("Classify: %v %+v", err, a)
	}
}

func TestCRUD_Decisions(t *testing.T) {
	c := crudStub(t, nil)
	list, err := c.ListDecisions(context.Background(), 10)
	if err != nil || len(list) != 1 || list[0].ID != "d1" {
		t.Fatalf("ListDecisions: %v %+v", err, list)
	}
	one, err := c.GetDecision(context.Background(), "d1")
	if err != nil || one == nil || one.Statement != "ship it" {
		t.Fatalf("GetDecision: %v %+v", err, one)
	}
}
