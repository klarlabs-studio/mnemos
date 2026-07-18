package query

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// TestCorrectiveGate_RelaxesMinTrustOnInsufficient is the core R3 guarantee: a
// real but low-trust claim excluded by the caller's MinTrust on the first pass is
// RECOVERED by the corrective pass, which relaxes the soft MinTrust gate and
// widens to the whole corpus. Without the gate the query would return nothing.
func TestCorrectiveGate_RelaxesMinTrustOnInsufficient(t *testing.T) {
	var searchCalls, listAllCalls, byIDsCalls int
	now := time.Now().UTC()
	e1 := domain.Event{ID: "e1", Content: "deploy reverted after error spike"}
	events := spyEventRepo{
		byID:         map[string]domain.Event{"e1": e1},
		all:          []domain.Event{e1},
		listAllCount: &listAllCalls,
		byIDsCount:   &byIDsCalls,
	}
	// Low-trust but real claim: excluded by MinTrust=0.5 on the first pass,
	// recovered when the corrective gate drops the MinTrust floor.
	claims := fakeClaimRepo{claims: []domain.Claim{{
		ID: "cl1", Text: "the error spike was caused by the reverted deploy",
		TrustScore: 0.2, Confidence: 0.2, CreatedAt: now, ValidFrom: now,
	}}}
	repo := spyVectorRepo{hits: []ports.EventSimilarityHit{{EventID: "e1", Similarity: 0.9}}, called: &searchCalls}
	engine := NewEngine(events, claims, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithEmbeddings(repo, fakeEmbedClient{})

	ans, err := engine.AnswerWithOptions(context.Background(), "what caused the error spike?", AnswerOptions{MinTrust: 0.5})
	if err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	if len(ans.Claims) != 1 || ans.Claims[0].ID != "cl1" {
		t.Fatalf("corrective gate should recover the low-trust claim after relaxing MinTrust; got %d claims", len(ans.Claims))
	}
	if listAllCalls != 1 {
		t.Fatalf("corrective pass should widen to the corpus exactly once, got %d ListAll calls", listAllCalls)
	}
}

// TestCorrectiveGate_NoRetryWhenNothingToRecover proves the gate is bounded and
// doesn't thrash: when the first (whole-corpus) pass is already insufficient and
// there's no soft filter to relax, it does NOT make a second corpus pass.
func TestCorrectiveGate_NoRetryWhenNothingToRecover(t *testing.T) {
	var listAllCalls, byIDsCalls int
	events := spyEventRepo{
		byID:         map[string]domain.Event{},
		all:          []domain.Event{{ID: "e1", Content: "nothing relevant here"}},
		listAllCount: &listAllCalls,
		byIDsCount:   &byIDsCalls,
	}
	// No embedder/vector → whole-corpus path; no claims → insufficient; MinTrust
	// unset → nothing to relax → the gate must not fire a second pass.
	engine := NewEngine(events, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}})

	if _, err := engine.Answer(context.Background(), "anything?"); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if listAllCalls != 1 {
		t.Fatalf("gate must not re-query when nothing can be relaxed, got %d ListAll calls", listAllCalls)
	}
}

func TestRecallInsufficient(t *testing.T) {
	if !recallInsufficient(domain.Answer{}) {
		t.Fatal("an answer with no claims must be insufficient")
	}
	if !recallInsufficient(domain.Answer{Claims: []domain.Claim{{}}, Confidence: recallSufficiencyFloor - 0.01}) {
		t.Fatal("below-floor confidence must be insufficient")
	}
	if recallInsufficient(domain.Answer{Claims: []domain.Claim{{}}, Confidence: recallSufficiencyFloor + 0.1}) {
		t.Fatal("a confident, non-empty answer must be sufficient")
	}
}

func TestRelaxRecall_OnlySoftFilter(t *testing.T) {
	in := AnswerOptions{
		MinTrust:  0.7,
		Scope:     domain.Scope{Service: "payments"},
		Lifecycle: domain.ClaimLifecyclePromoted,
	}
	out, changed := relaxRecall(in)
	if !changed || out.MinTrust != 0 {
		t.Fatal("MinTrust must be relaxed to 0")
	}
	// Semantic filters must be preserved — relaxing them would surface claims the
	// caller explicitly excluded.
	if out.Scope.Service != "payments" || out.Lifecycle != domain.ClaimLifecyclePromoted {
		t.Fatalf("semantic filters must NOT be relaxed, got %+v", out)
	}
	if _, changed := relaxRecall(AnswerOptions{}); changed {
		t.Fatal("nothing to relax when MinTrust is unset")
	}
}

func TestStrongerAnswer(t *testing.T) {
	empty := domain.Answer{}
	one := domain.Answer{Claims: []domain.Claim{{}}, Confidence: 0.3}
	two := domain.Answer{Claims: []domain.Claim{{}, {}}, Confidence: 0.1}
	moreConf := domain.Answer{Claims: []domain.Claim{{}}, Confidence: 0.6}

	if strongerAnswer(empty, one) {
		t.Fatal("an empty corrected answer must never replace a non-empty one")
	}
	if !strongerAnswer(one, empty) {
		t.Fatal("a non-empty corrected answer must replace an empty one")
	}
	if !strongerAnswer(two, one) {
		t.Fatal("more claims must win")
	}
	if !strongerAnswer(moreConf, one) {
		t.Fatal("higher confidence must break a claim-count tie")
	}
	if strongerAnswer(one, moreConf) {
		t.Fatal("lower confidence must not replace higher at equal claim count")
	}
}
