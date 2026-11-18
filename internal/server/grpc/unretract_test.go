package grpc_test

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UnretractEpisode restores what the retraction took, attributes it to the
// token subject, and is idempotent (ADR 0037 §8).
func TestGRPC_UnretractEpisode(t *testing.T) {
	client, conn, issuer := startSQLiteAuthServer(t)
	seedRetractable(t, conn)
	ctx := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, nil))
	if _, err := client.RetractEpisode(ctx, &mnemosv1.RetractEpisodeRequest{EpisodeId: "ev-mine", Reason: "bad source"}); err != nil {
		t.Fatalf("RetractEpisode: %v", err)
	}

	got, err := client.UnretractEpisode(ctx, &mnemosv1.UnretractEpisodeRequest{EpisodeId: "ev-mine", Reason: "good source"})
	if err != nil {
		t.Fatalf("UnretractEpisode: %v", err)
	}
	if got.GetEpisodeId() != "ev-mine" || got.GetAlreadyUnretracted() || got.GetReason() != "good source" ||
		got.GetUnretractedBy() != "agent-1" || got.GetGeneration() != 1 {
		t.Errorf("response = %+v, want a first un-retraction of ev-mine by agent-1", got)
	}
	im := got.GetImpact()
	if r := im.GetRestoredBeliefs(); len(r) != 1 || r[0].GetId() != "cl-only" || r[0].GetStatus() != string(domain.ClaimStatusActive) {
		t.Errorf("restored = %v, want cl-only back to active", r)
	}
	if r := im.GetRelinkedBeliefs(); len(r) != 1 || r[0].GetId() != "cl-shared" {
		t.Errorf("relinked = %v, want cl-shared", r)
	}
	claims, err := conn.Claims.ListByIDs(context.Background(), []string{"cl-only"})
	if err != nil || len(claims) != 1 || claims[0].Status != domain.ClaimStatusActive {
		t.Errorf("cl-only = %+v (%v), want active in the store", claims, err)
	}
	if _, ok, err := govwrite.LookupRetraction(context.Background(), conn, "ev-mine"); err != nil || ok {
		t.Errorf("ev-mine still retracted (ok=%v err=%v)", ok, err)
	}

	again, err := client.UnretractEpisode(ctx, &mnemosv1.UnretractEpisodeRequest{EpisodeId: "ev-mine"})
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if !again.GetAlreadyUnretracted() || again.GetReason() != "good source" {
		t.Errorf("repeat = %+v, want the stored record marked already un-retracted", again)
	}
}

func TestGRPC_UnretractEpisode_Refusals(t *testing.T) {
	client, conn, issuer := startSQLiteAuthServer(t)
	seedRetractable(t, conn)
	write := []string{domain.ScopeClaimsWrite}
	admin := bearerCtx(t, mintToken(t, issuer, write, nil))
	for _, id := range []string{"ev-mine", "ev-theirs"} {
		if _, err := client.RetractEpisode(admin, &mnemosv1.RetractEpisodeRequest{EpisodeId: id}); err != nil {
			t.Fatalf("retract %s: %v", id, err)
		}
	}

	cases := []struct {
		name  string
		token string
		id    string
		want  codes.Code
	}{
		{"claims:read token", mintToken(t, issuer, []string{"claims:read"}, nil), "ev-mine", codes.PermissionDenied},
		{"unknown episode", mintToken(t, issuer, write, nil), "ev-missing", codes.NotFound},
		{"episode in another run", mintToken(t, issuer, write, []string{"run-mine"}), "ev-theirs", codes.NotFound},
		{"never retracted", mintToken(t, issuer, write, nil), "ev-other", codes.FailedPrecondition},
		{"no episode id", mintToken(t, issuer, write, nil), " ", codes.InvalidArgument},
	}
	for _, tc := range cases {
		_, err := client.UnretractEpisode(bearerCtx(t, tc.token), &mnemosv1.UnretractEpisodeRequest{EpisodeId: tc.id})
		if got := status.Code(err); got != tc.want {
			t.Errorf("%s: code = %v (%v), want %v", tc.name, got, err, tc.want)
		}
	}
	for _, id := range []string{"ev-mine", "ev-theirs"} {
		if _, ok, err := govwrite.LookupRetraction(context.Background(), conn, id); err != nil || !ok {
			t.Errorf("%s un-retracted by a refused call (ok=%v err=%v)", id, ok, err)
		}
	}
}
