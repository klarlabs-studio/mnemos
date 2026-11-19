package grpc_test

import (
	"context"
	"testing"

	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
)

// ADR 0041 over gRPC, in parity with POST /v1/beliefs: a new id restating a
// held belief is stored as a redirect to it, a re-append of that id stays a
// redirect, and the response says where each id went.
func TestAppendBeliefs_ReportsRemapOntoHeldBelief(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()
	ctx := context.Background()

	appendBelief := func(id string) *mnemosv1.AppendResponse {
		t.Helper()
		resp, err := client.AppendBeliefs(ctx, &mnemosv1.AppendBeliefsRequest{Beliefs: []*mnemosv1.Belief{{
			Id: id, Text: "the billing service retries failed charges three times",
			Type: "fact", Confidence: 0.8, Status: "active",
		}}})
		if err != nil {
			t.Fatalf("AppendBeliefs(%s): %v", id, err)
		}
		return resp
	}

	if r := appendBelief("cl-brain-a"); r.Accepted != 1 || r.Folded != 0 || len(r.Remapped) != 0 {
		t.Fatalf("first write = %+v, want accepted=1 and no remap", r)
	}
	r := appendBelief("cl-brain-b")
	if r.Accepted != 1 || r.Folded != 1 || r.Remapped["cl-brain-b"] != "cl-brain-a" {
		t.Fatalf("restatement = %+v, want folded=1 and cl-brain-b -> cl-brain-a", r)
	}
	r = appendBelief("cl-brain-b")
	if r.Folded != 0 || r.Remapped["cl-brain-b"] != "cl-brain-a" {
		t.Fatalf("re-append = %+v, want folded=0 and cl-brain-b -> cl-brain-a", r)
	}

	list, err := client.ListBeliefs(ctx, &mnemosv1.ListBeliefsRequest{})
	if err != nil {
		t.Fatalf("ListBeliefs: %v", err)
	}
	status := map[string]string{}
	for _, b := range list.Beliefs {
		status[b.Id] = b.Status
	}
	if status["cl-brain-a"] != "active" || status["cl-brain-b"] != "deprecated" {
		t.Fatalf("statuses = %v, want cl-brain-a active and cl-brain-b deprecated", status)
	}
}
