package query

import (
	"context"
	"fmt"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// eventScopedClaims returns only the claims evidenced by the requested events,
// as the real stores do; fakeClaimRepo returns every claim for any event list,
// which cannot reproduce the episode gate.
type eventScopedClaims struct{ fakeClaimRepo }

func (f eventScopedClaims) ListByEventIDs(_ context.Context, eventIDs []string) ([]domain.Claim, error) {
	want := map[string]struct{}{}
	for _, id := range eventIDs {
		want[id] = struct{}{}
	}
	keep := map[string]struct{}{}
	for _, ev := range f.evidence {
		if _, ok := want[ev.EventID]; ok {
			keep[ev.ClaimID] = struct{}{}
		}
	}
	var out []domain.Claim
	for _, c := range f.claims {
		if _, ok := keep[c.ID]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// A brain where six episodes outrank the one that holds the answer. Recall
// used to consider only beliefs from the top answerEventLimit (5) episodes, so
// the answer could never be recalled however well it matched (#456, ADR 0030).
func widenFixture() (events []domain.Event, claims []domain.Claim, evidence []domain.ClaimEvidence) {
	for i := range 6 {
		id := fmt.Sprintf("ev_decoy_%d", i)
		events = append(events, domain.Event{ID: id, RunID: "r1", Content: "Joanna baked a cake and talked about the cake recipe and cake decorating"})
		cid := fmt.Sprintf("cl_decoy_%d", i)
		claims = append(claims, domain.Claim{ID: cid, Text: fmt.Sprintf("Joanna likes cake decorating %d", i), Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, TrustScore: 0.6})
		evidence = append(evidence, domain.ClaimEvidence{ClaimID: cid, EventID: id})
	}
	events = append(events, domain.Event{ID: "ev_answer", RunID: "r2", Content: "unrelated chatter about the weekend"})
	claims = append(claims, domain.Claim{ID: "cl_answer", Text: "Joanna used strawberry filling in the cake", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, TrustScore: 0.6})
	evidence = append(evidence, domain.ClaimEvidence{ClaimID: "cl_answer", EventID: "ev_answer"})
	return events, claims, evidence
}

func widenEngine(events []domain.Event, claims []domain.Claim, evidence []domain.ClaimEvidence) Engine {
	return NewEngine(fakeEventRepo{events: events}, eventScopedClaims{fakeClaimRepo{claims: claims, evidence: evidence}}, fakeRelationshipRepo{}).
		WithTextSearch(nil, fakeTextSearcher{hits: []ports.TextHit{{ID: "cl_answer", Score: 9}}})
}

func recalled(ans domain.Answer, id string) bool {
	for _, c := range ans.Claims {
		if c.ID == id {
			return true
		}
	}
	return false
}

func TestRecall_ReachesBeliefsOutsideTheTopEpisodes(t *testing.T) {
	events, claims, evidence := widenFixture()
	e := widenEngine(events, claims, evidence)
	ans, err := e.AnswerWithOptions(context.Background(), "What filling did Joanna use in the cake?", AnswerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !recalled(ans, "cl_answer") {
		t.Fatalf("the belief that answers the question was not recalled; recall is still gated on the top episodes: %d claims", len(ans.Claims))
	}
}

func TestRunScopedRecall_IsNotWidened(t *testing.T) {
	events, claims, evidence := widenFixture()
	e := widenEngine(events, claims, evidence)
	ans, err := e.AnswerForRunWithOptions(context.Background(), "What filling did Joanna use in the cake?", "r1", AnswerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if recalled(ans, "cl_answer") {
		t.Fatal("run-scoped recall for r1 returned a belief from run r2")
	}
}
