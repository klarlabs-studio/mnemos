package grpc_test

import (
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Declare, list and remove an episode dependency over gRPC (ADR 0038): the
// change is attributed to the token subject, the dependent episode's beliefs
// are rescored, and removal leaves a tombstone the audit listing shows.
func TestGRPC_EpisodeDependencies(t *testing.T) {
	client, conn, issuer := startSQLiteAuthServer(t)
	seedRetractable(t, conn)
	ctx := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, nil))

	got, err := client.DeclareEpisodeDependency(ctx, &mnemosv1.DeclareEpisodeDependencyRequest{
		EpisodeId: "ev-other", DependsOn: "ev-mine", Kind: "copies", Note: "a repost",
	})
	if err != nil {
		t.Fatalf("DeclareEpisodeDependency: %v", err)
	}
	d := got.GetDependency()
	if d.GetEpisodeId() != "ev-other" || d.GetDependsOn() != "ev-mine" || d.GetSignal() != "declared" || d.GetCreatedBy() != "agent-1" ||
		d.GetReference() != "a repost" || d.GetRemovedAt() != nil {
		t.Errorf("declared = %+v", d)
	}
	if r := got.GetRescoredBeliefs(); len(r) != 1 || r[0] != "cl-shared" {
		t.Errorf("rescored = %v, want [cl-shared]", r)
	}

	list, err := client.ListEpisodeDependencies(ctx, &mnemosv1.ListEpisodeDependenciesRequest{EpisodeId: "ev-mine"})
	if err != nil || len(list.GetDependents()) != 1 || list.GetDependents()[0].GetEpisodeId() != "ev-other" {
		t.Fatalf("ListEpisodeDependencies = %+v (%v)", list, err)
	}

	if _, err := client.RemoveEpisodeDependency(ctx, &mnemosv1.RemoveEpisodeDependencyRequest{
		EpisodeId: "ev-other", DependsOn: "ev-mine", Reason: strings.Repeat("x", domain.MaxDependencyRemovalReasonLen+1),
	}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("over-long reason: err = %v, want InvalidArgument", err)
	}
	removed, err := client.RemoveEpisodeDependency(ctx, &mnemosv1.RemoveEpisodeDependencyRequest{EpisodeId: "ev-other", DependsOn: "ev-mine", Reason: "not a repost"})
	if err != nil {
		t.Fatalf("RemoveEpisodeDependency: %v", err)
	}
	if removed.GetDependency().GetRemovedAt() == nil || removed.GetDependency().GetRemovedBy() != "agent-1" || removed.GetDependency().GetRemovalReason() != "not a repost" {
		t.Errorf("removed = %+v", removed.GetDependency())
	}
	live, err := conn.Dependencies.ListByEventIDs(context.Background(), []string{"ev-other"})
	if err != nil || len(live) != 0 {
		t.Errorf("live rows after removal = %+v (%v)", live, err)
	}
	audit, err := client.ListEpisodeDependencies(ctx, &mnemosv1.ListEpisodeDependenciesRequest{EpisodeId: "ev-mine", IncludeRemoved: true})
	if err != nil || len(audit.GetDependents()) != 1 || audit.GetDependents()[0].GetRemovedAt() == nil || audit.GetDependents()[0].GetRemovalReason() != "not a repost" {
		t.Errorf("audit = %+v (%v)", audit, err)
	}

	// Declared again: the removed interval stays in the audit listing.
	if _, err := client.DeclareEpisodeDependency(ctx, &mnemosv1.DeclareEpisodeDependencyRequest{EpisodeId: "ev-other", DependsOn: "ev-mine", Kind: "cites"}); err != nil {
		t.Fatalf("revive: %v", err)
	}
	audit, err = client.ListEpisodeDependencies(ctx, &mnemosv1.ListEpisodeDependenciesRequest{EpisodeId: "ev-mine", IncludeRemoved: true})
	if err != nil || len(audit.GetDependents()) != 2 || audit.GetDependents()[0].GetRemovalReason() != "not a repost" || audit.GetDependents()[1].GetRemovedAt() != nil {
		t.Errorf("audit after revival = %+v (%v), want the removed interval then the live one", audit, err)
	}
}

func TestGRPC_EpisodeDependencies_Refusals(t *testing.T) {
	client, conn, issuer := startSQLiteAuthServer(t)
	seedRetractable(t, conn)
	write := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, nil))
	scoped := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, []string{"run-mine"}))
	if _, err := client.DeclareEpisodeDependency(write, &mnemosv1.DeclareEpisodeDependencyRequest{EpisodeId: "ev-other", DependsOn: "ev-mine", Kind: "cites"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cases := []struct {
		name string
		call func() error
		want codes.Code
	}{
		{"live pair", func() error {
			_, err := client.DeclareEpisodeDependency(write, &mnemosv1.DeclareEpisodeDependencyRequest{EpisodeId: "ev-other", DependsOn: "ev-mine", Kind: "copies"})
			return err
		}, codes.AlreadyExists},
		{"bad kind", func() error {
			_, err := client.DeclareEpisodeDependency(write, &mnemosv1.DeclareEpisodeDependencyRequest{EpisodeId: "ev-mine", DependsOn: "ev-other", Kind: "echoes"})
			return err
		}, codes.InvalidArgument},
		{"no episode id", func() error {
			_, err := client.ListEpisodeDependencies(write, &mnemosv1.ListEpisodeDependenciesRequest{})
			return err
		}, codes.InvalidArgument},
		{"source in another run", func() error {
			_, err := client.DeclareEpisodeDependency(scoped, &mnemosv1.DeclareEpisodeDependencyRequest{EpisodeId: "ev-mine", DependsOn: "ev-theirs", Kind: "cites"})
			return err
		}, codes.NotFound},
		{"list another run", func() error {
			_, err := client.ListEpisodeDependencies(scoped, &mnemosv1.ListEpisodeDependenciesRequest{EpisodeId: "ev-theirs"})
			return err
		}, codes.NotFound},
		{"remove a pair never declared", func() error {
			_, err := client.RemoveEpisodeDependency(write, &mnemosv1.RemoveEpisodeDependencyRequest{EpisodeId: "ev-mine", DependsOn: "ev-other"})
			return err
		}, codes.NotFound},
	}
	for _, tc := range cases {
		if got := status.Code(tc.call()); got != tc.want {
			t.Errorf("%s: code = %s, want %s", tc.name, got, tc.want)
		}
	}
}
