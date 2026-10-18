package grpc_test

import (
	"context"
	"math"
	"testing"
	"time"

	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// gRPC parity with POST /v1/beliefs: a dated record arriving over gRPC must
// keep its valid_from and half-life, and valid_to must close it. Without these
// fields a gRPC writer's dated records decay on the 90-day default like any
// passing remark, which is the trust-decay pattern REST was fixed for.
func TestAppendBeliefs_CarriesValidityAndHalfLife(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx := context.Background()
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	if _, err := client.AppendBeliefs(ctx, &mnemosv1.AppendBeliefsRequest{
		Beliefs: []*mnemosv1.Belief{
			{
				Id: "cl-dated", Text: "dated record", Type: "fact", Confidence: 0.9, Status: "active",
				ValidFrom: timestamppb.New(from), ValidTo: timestamppb.New(to), HalfLifeDays: 3650,
			},
			{Id: "cl-plain", Text: "plain", Type: "fact", Confidence: 0.9, Status: "active"},
		},
	}); err != nil {
		t.Fatalf("AppendBeliefs: %v", err)
	}

	list, err := client.ListBeliefs(ctx, &mnemosv1.ListBeliefsRequest{})
	if err != nil {
		t.Fatalf("ListBeliefs: %v", err)
	}

	got := map[string]*mnemosv1.Belief{}
	for _, b := range list.Beliefs {
		got[b.Id] = b
	}

	dated := got["cl-dated"]
	if dated == nil {
		t.Fatalf("cl-dated missing from %v", list.Beliefs)
	}
	if !dated.GetValidFrom().AsTime().Equal(from) {
		t.Errorf("valid_from = %v, want %v", dated.GetValidFrom().AsTime(), from)
	}
	if !dated.GetValidTo().AsTime().Equal(to) {
		t.Errorf("valid_to = %v, want %v", dated.GetValidTo().AsTime(), to)
	}
	if dated.HalfLifeDays != 3650 {
		t.Errorf("half_life_days = %v, want 3650", dated.HalfLifeDays)
	}

	plain := got["cl-plain"]
	if plain == nil {
		t.Fatalf("cl-plain missing from %v", list.Beliefs)
	}
	// valid_from defaults to created_at in the store; nothing else is set.
	if plain.ValidTo != nil || plain.HalfLifeDays != 0 {
		t.Errorf("undated belief grew validity: to=%v half_life=%v", plain.ValidTo, plain.HalfLifeDays)
	}

	// valid_to closes the belief for as_of reads after it.
	after, err := client.ListBeliefs(ctx, &mnemosv1.ListBeliefsRequest{AsOf: timestamppb.New(to.AddDate(0, 1, 0))})
	if err != nil {
		t.Fatalf("ListBeliefs as_of: %v", err)
	}
	for _, b := range after.Beliefs {
		if b.Id == "cl-dated" {
			t.Errorf("cl-dated still visible after its valid_to")
		}
	}
}

// A re-append that omits valid_to must not reopen a closed belief: valid_to
// has its own governed write, exactly as on REST.
func TestAppendBeliefs_ReappendDoesNotReopen(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx := context.Background()
	to := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	belief := func(validTo *timestamppb.Timestamp) *mnemosv1.AppendBeliefsRequest {
		return &mnemosv1.AppendBeliefsRequest{Beliefs: []*mnemosv1.Belief{
			{Id: "cl-x", Text: "x", Type: "fact", Confidence: 0.9, Status: "active", ValidTo: validTo},
		}}
	}

	if _, err := client.AppendBeliefs(ctx, belief(timestamppb.New(to))); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := client.AppendBeliefs(ctx, belief(nil)); err != nil {
		t.Fatalf("re-append: %v", err)
	}

	list, err := client.ListBeliefs(ctx, &mnemosv1.ListBeliefsRequest{})
	if err != nil {
		t.Fatalf("ListBeliefs: %v", err)
	}
	if len(list.Beliefs) != 1 || !list.Beliefs[0].GetValidTo().AsTime().Equal(to) {
		t.Fatalf("re-append reopened the belief: %v", list.Beliefs)
	}
}

func TestAppendBeliefs_RejectsInvalidValidity(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	from := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]*mnemosv1.Belief{
		"valid_to before valid_from": {ValidFrom: timestamppb.New(from), ValidTo: timestamppb.New(from.AddDate(0, 0, -1))},
		"negative half-life":         {HalfLifeDays: -1},
		"NaN half-life":              {HalfLifeDays: math.NaN()},
		"infinite half-life":         {HalfLifeDays: math.Inf(1)},
	}

	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			b.Id, b.Text, b.Type, b.Status = "cl-bad", "bad", "fact", "active"

			_, err := client.AppendBeliefs(context.Background(), &mnemosv1.AppendBeliefsRequest{Beliefs: []*mnemosv1.Belief{b}})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("want InvalidArgument, got %v", err)
			}
		})
	}
}
