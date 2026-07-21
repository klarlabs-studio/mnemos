package pipeline

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// Embedding the port means only the two methods the filter uses need
// implementing; anything else it reached for would panic loudly rather than
// pass silently.
type fakeEvents struct {
	ports.EventRepository
	events []domain.Event
}

func (f fakeEvents) ListByIDs(_ context.Context, ids []string) ([]domain.Event, error) {
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var out []domain.Event
	for _, e := range f.events {
		if _, ok := want[e.ID]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

type fakeClaims struct {
	ports.ClaimRepository
	links []domain.ClaimEvidence
}

func (f fakeClaims) ListEvidenceByClaimIDs(_ context.Context, ids []string) ([]domain.ClaimEvidence, error) {
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var out []domain.ClaimEvidence
	for _, l := range f.links {
		if _, ok := want[l.ClaimID]; ok {
			out = append(out, l)
		}
	}
	return out, nil
}

// fixture: c_old belongs to session S1, c_other to session S2, c_untagged to
// an ingestion that predates session tagging. c_new is written by the run.
func scopeFixture() (fakeEvents, fakeClaims) {
	ev := fakeEvents{events: []domain.Event{
		{ID: "e1", Metadata: map[string]string{SessionMetadataKey: "S1"}},
		{ID: "e2", Metadata: map[string]string{SessionMetadataKey: "S2"}},
		{ID: "e3"},
	}}
	cl := fakeClaims{links: []domain.ClaimEvidence{
		{ClaimID: "c_old", EventID: "e1"},
		{ClaimID: "c_other", EventID: "e2"},
		{ClaimID: "c_untagged", EventID: "e3"},
	}}
	return ev, cl
}

func contradicts(from, to string) domain.Relationship {
	return domain.Relationship{Type: domain.RelationshipTypeContradicts, FromClaimID: from, ToClaimID: to}
}

func TestDropIntraSessionContradictions(t *testing.T) {
	ev, cl := scopeFixture()
	newIDs := map[string]struct{}{"c_new": {}}

	tests := []struct {
		name string
		rel  domain.Relationship
		want bool // want the edge kept
	}{
		{
			// The case this exists for: a conversation arguing with itself.
			name: "same session is dropped",
			rel:  contradicts("c_new", "c_old"),
			want: false,
		},
		{
			// Two sessions asserting incompatible things is a real conflict.
			name: "different session is kept",
			rel:  contradicts("c_new", "c_other"),
			want: true,
		},
		{
			// Never suppress on a guess: without a session on both sides the
			// filter cannot know they came from one conversation.
			name: "unknown session is kept",
			rel:  contradicts("c_new", "c_untagged"),
			want: true,
		},
		{
			// A conversation corroborating itself is real evidence.
			name: "supports within a session is kept",
			rel:  domain.Relationship{Type: domain.RelationshipTypeSupports, FromClaimID: "c_new", ToClaimID: "c_old"},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, dropped, err := DropIntraSessionContradictions(
				context.Background(), ev, cl, []domain.Relationship{tc.rel}, "S1", newIDs)
			if err != nil {
				t.Fatalf("DropIntraSessionContradictions: %v", err)
			}
			if kept := len(got) == 1; kept != tc.want {
				t.Fatalf("kept=%v want %v (dropped=%d)", kept, tc.want, dropped)
			}
		})
	}
}

// TestDropIntraSessionContradictions_NoSessionIsNoOp pins that every other
// ingestion path — CLI process, git ingest, the MCP tool without a session —
// is completely unaffected.
func TestDropIntraSessionContradictions_NoSessionIsNoOp(t *testing.T) {
	ev, cl := scopeFixture()
	rels := []domain.Relationship{contradicts("c_new", "c_old")}
	got, dropped, err := DropIntraSessionContradictions(
		context.Background(), ev, cl, rels, "", map[string]struct{}{"c_new": {}})
	if err != nil {
		t.Fatalf("DropIntraSessionContradictions: %v", err)
	}
	if len(got) != 1 || dropped != 0 {
		t.Fatalf("no session id must change nothing, got %d edges dropped=%d", len(got), dropped)
	}
}

// TestDropIntraSessionContradictions_BothPreExisting covers the pair the
// capture path actually generates most: two claims from EARLIER chunks of the
// same session, neither of them new in this run. Incremental capture splits
// one conversation across many runs, so this is the common shape, not an edge
// case.
func TestDropIntraSessionContradictions_BothPreExisting(t *testing.T) {
	ev := fakeEvents{events: []domain.Event{
		{ID: "e1", Metadata: map[string]string{SessionMetadataKey: "S1"}},
		{ID: "e2", Metadata: map[string]string{SessionMetadataKey: "S1"}},
	}}
	cl := fakeClaims{links: []domain.ClaimEvidence{
		{ClaimID: "c_a", EventID: "e1"},
		{ClaimID: "c_b", EventID: "e2"},
	}}
	got, dropped, err := DropIntraSessionContradictions(
		context.Background(), ev, cl, []domain.Relationship{contradicts("c_a", "c_b")}, "S1", nil)
	if err != nil {
		t.Fatalf("DropIntraSessionContradictions: %v", err)
	}
	if len(got) != 0 || dropped != 1 {
		t.Fatalf("two claims from earlier chunks of the same session must drop, got %d edges dropped=%d", len(got), dropped)
	}
}
