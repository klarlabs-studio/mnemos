package grpc_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/store"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ADR 0035 step 2: gRPC ListBeliefs with recorded_as_of reads the store as it
// was at T through the same views as recall and the REST browse. Before it,
// it filtered today's rows on created_at: a belief retracted after T came back
// deprecated, a belief merged after T came back as a redirect, and a run
// filter dropped the merged belief because its evidence had moved.
func TestListBeliefs_RecordedAsOfRewindsStatusAndMerges(t *testing.T) {
	client, conn, cleanup := startTestServerWithConn(t)
	defer cleanup()
	ctx := context.Background()
	tuesday := seedRecordedTimeline(t, conn)

	for _, tc := range []struct {
		name string
		req  *mnemosv1.ListBeliefsRequest
	}{
		{"active", &mnemosv1.ListBeliefsRequest{RecordedAsOf: timestamppb.New(tuesday), StatusFilter: "active", IncludeEvidence: true}},
		{"run scoped", &mnemosv1.ListBeliefsRequest{RecordedAsOf: timestamppb.New(tuesday), RunId: "timeline", IncludeEvidence: true}},
	} {
		list, err := client.ListBeliefs(ctx, tc.req)
		if err != nil {
			t.Fatalf("%s: ListBeliefs: %v", tc.name, err)
		}
		var ids []string
		for _, b := range list.Beliefs {
			ids = append(ids, b.Id)
			if b.Status != string(domain.ClaimStatusActive) {
				t.Errorf("%s: %s reads as %q at T, want active", tc.name, b.Id, b.Status)
			}
		}
		sort.Strings(ids)
		want := []string{"cl-held", "cl-merged", "cl-retracted"}
		if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
			t.Errorf("%s: recorded_as_of ListBeliefs = %v, want %v", tc.name, ids, want)
		}
		evidence := map[string]string{}
		for _, e := range list.Evidence {
			evidence[e.BeliefId] = e.EpisodeId
		}
		if evidence["cl-merged"] != "ev-merged" {
			t.Errorf("%s: cl-merged's evidence at T = %q, want ev-merged", tc.name, evidence["cl-merged"])
		}
	}
}

// seedRecordedTimeline records three beliefs, then after the returned instant
// retracts one, folds another into a restatement, and records a new one.
func seedRecordedTimeline(t *testing.T, conn *store.Conn) time.Time {
	t.Helper()
	ctx := context.Background()
	add := func(eventID, claimID, text string) {
		t.Helper()
		now := time.Now().UTC()
		if err := conn.Events.Append(ctx, domain.Event{
			ID: eventID, RunID: "timeline", SchemaVersion: "v1", SourceInputID: "timeline",
			Content: text, Timestamp: now, IngestedAt: now,
		}); err != nil {
			t.Fatalf("append %s: %v", eventID, err)
		}
		if err := conn.Claims.Upsert(ctx, []domain.Claim{{
			ID: claimID, Text: text, Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive,
			Confidence: 0.8, CreatedAt: now,
		}}); err != nil {
			t.Fatalf("upsert %s: %v", claimID, err)
		}
		if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: claimID, EventID: eventID}}); err != nil {
			t.Fatalf("evidence %s: %v", claimID, err)
		}
	}
	add("ev-held", "cl-held", "the payments pool size is 50")
	add("ev-retracted", "cl-retracted", "the outage was caused by disk saturation")
	add("ev-merged", "cl-merged", "deploys go out on tuesdays")
	time.Sleep(5 * time.Millisecond)
	tuesday := time.Now().UTC()
	time.Sleep(5 * time.Millisecond)

	retracted, err := conn.Claims.ListByIDs(ctx, []string{"cl-retracted"})
	if err != nil || len(retracted) != 1 {
		t.Fatalf("load cl-retracted: %v", err)
	}
	retracted[0].Status = domain.ClaimStatusDeprecated
	if err := conn.Claims.UpsertWithReason(ctx, retracted, "retracted"); err != nil {
		t.Fatalf("retract: %v", err)
	}
	add("ev-winner", "cl-winner", "deploys happen every tuesday")
	if _, err := pipeline.ApplySemanticDedupe(ctx, conn, pipeline.SemanticDedupePlan{
		Merges: []pipeline.SemanticMerge{{WinnerID: "cl-winner", DuplicateIDs: []string{"cl-merged"}}},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	add("ev-new", "cl-new", "the payments pool size is 80")
	return tuesday
}
