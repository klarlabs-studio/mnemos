package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	client "go.klarlabs.de/mnemos/client"
)

func TestRecall_ModesAndParams(t *testing.T) {
	var lastQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.RawQuery
		mode := r.URL.Query().Get("mode")
		resp := client.RecallResponse{Mode: mode, Results: []client.RecallResult{{ClaimID: "c1", Text: "x"}}}
		switch mode {
		case "sufficiency":
			resp.Sufficiency = &client.Sufficiency{Sufficient: true, ClaimCount: 1}
		case "effort":
			resp.Sufficiency = &client.Sufficiency{}
			resp.Effort = &client.EffortReport{Passes: 2}
		case "conflicts":
			resp.Conflicts = []client.Conflict{{ClaimID: "c1", ContradictingID: "c2"}}
		case "iterative":
			resp.Rounds = 3
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	c := client.New(srv.URL)
	ctx := context.Background()

	suf, err := c.Recall(ctx, client.RecallRequest{Query: "creatinine", Mode: "sufficiency"})
	if err != nil || suf == nil || len(suf.Results) != 1 || suf.Sufficiency == nil || !suf.Sufficiency.Sufficient {
		t.Fatalf("sufficiency: %v %+v", err, suf)
	}

	eff, err := c.Recall(ctx, client.RecallRequest{Query: "x", Mode: "effort", Stakes: 0.9})
	if err != nil || eff.Effort == nil || eff.Effort.Passes != 2 {
		t.Fatalf("effort: %v %+v", err, eff)
	}
	if !contains(lastQuery, "stakes=0.9") {
		t.Errorf("stakes not sent: %q", lastQuery)
	}

	conf, err := c.Recall(ctx, client.RecallRequest{Query: "x", Mode: "conflicts"})
	if err != nil || len(conf.Conflicts) != 1 || conf.Conflicts[0].ContradictingID != "c2" {
		t.Fatalf("conflicts: %v %+v", err, conf)
	}

	it, err := c.Recall(ctx, client.RecallRequest{Query: "x", Mode: "iterative", MaxRounds: 5})
	if err != nil || it.Rounds != 3 {
		t.Fatalf("iterative: %v %+v", err, it)
	}
	if !contains(lastQuery, "max_rounds=5") {
		t.Errorf("max_rounds not sent: %q", lastQuery)
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
