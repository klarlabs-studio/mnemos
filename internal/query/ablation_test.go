package query

import (
	"context"
	"reflect"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// recallIDs runs one recall through the single entry point and returns the
// claim ids in rank order.
func recallIDs(t *testing.T, e Engine, question string, opts AnswerOptions) []string {
	t.Helper()
	ans, err := e.Recall(context.Background(), RecallRequest{Question: question, Options: opts})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	ids := make([]string, len(ans.Claims))
	for i, c := range ans.Claims {
		ids[i] = c.ID
	}
	return ids
}

func TestAblation_ZeroValueLeavesTheEngineUnchanged(t *testing.T) {
	e := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{}).
		WithTextSearch(fakeTextSearcher{}, fakeTextSearcher{})
	if got := e.ablated(Ablation{}); !reflect.DeepEqual(got, e) {
		t.Fatalf("zero Ablation changed the engine:\n got %+v\nwant %+v", got, e)
	}
}

func TestAblation_DenseAndLexicalDropOnlyTheirLeg(t *testing.T) {
	e := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{}).
		WithTextSearch(fakeTextSearcher{}, fakeTextSearcher{}).
		WithEmbeddings(nil, stubQueryEmbedder{})

	noDense := e.ablated(Ablation{Dense: true})
	if noDense.embedClient != nil {
		t.Error("Dense ablation kept the embedding client")
	}
	if noDense.eventTextSearch == nil || noDense.claimTextSearch == nil {
		t.Error("Dense ablation dropped the lexical leg too")
	}

	noLex := e.ablated(Ablation{Lexical: true})
	if noLex.eventTextSearch != nil || noLex.claimTextSearch != nil {
		t.Error("Lexical ablation kept a text searcher")
	}
	if noLex.embedClient == nil {
		t.Error("Lexical ablation dropped the dense leg too")
	}
}

// Curation off admits what shipped recall removes: deprecated, superseded,
// archived and session-local claims.
func TestAblation_CurationAdmitsEveryStoredClaim(t *testing.T) {
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	claims := []domain.Claim{
		{ID: "cl_live", Text: "payments region", Status: domain.ClaimStatusActive, CreatedAt: past},
		{ID: "cl_dep", Text: "payments region", Status: domain.ClaimStatusDeprecated, CreatedAt: past},
		{ID: "cl_sup", Text: "payments region", Status: domain.ClaimStatusActive, CreatedAt: past, ValidFrom: past, ValidTo: now.Add(-time.Minute)},
		{ID: "cl_arc", Text: "payments region", Status: domain.ClaimStatusActive, CreatedAt: past, ArchivedAt: past},
		{ID: "cl_ses", Text: "payments region", Status: domain.ClaimStatusActive, CreatedAt: past, Durability: domain.DurabilitySessionLocal},
	}
	e := NewEngine(fakeEventRepo{events: []domain.Event{{ID: "ev1", Content: "payments region", Timestamp: past}}},
		fakeClaimRepo{claims: claims}, fakeRelationshipRepo{})

	if got := recallIDs(t, e, "payments region", AnswerOptions{}); !reflect.DeepEqual(got, []string{"cl_live"}) {
		t.Fatalf("shipped recall admitted %v, want only cl_live", got)
	}
	got := recallIDs(t, e, "payments region", AnswerOptions{Ablate: Ablation{Curation: true}})
	if len(got) != len(claims) {
		t.Fatalf("curation ablation admitted %v, want all %d claims", got, len(claims))
	}
}

// The caller's explicit point-in-time bound is not curation and survives the
// ablation: a claim that did not exist yet at AsOf stays out.
func TestAblation_CurationKeepsTheExplicitAsOfBound(t *testing.T) {
	now := time.Now().UTC()
	asOf := now.Add(-48 * time.Hour)
	claims := []domain.Claim{
		{ID: "cl_then", Text: "pool size", Status: domain.ClaimStatusDeprecated, CreatedAt: asOf.Add(-time.Hour), ValidFrom: asOf.Add(-time.Hour)},
		{ID: "cl_later", Text: "pool size", Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now.Add(-time.Hour)},
	}
	e := NewEngine(fakeEventRepo{events: []domain.Event{{ID: "ev1", Content: "pool size", Timestamp: asOf}}},
		fakeClaimRepo{claims: claims}, fakeRelationshipRepo{})

	got := recallIDs(t, e, "pool size", AnswerOptions{AsOf: asOf, Ablate: Ablation{Curation: true}})
	if !reflect.DeepEqual(got, []string{"cl_then"}) {
		t.Fatalf("got %v, want [cl_then]: deprecated admitted, the later claim still bounded out", got)
	}
}

func TestAblation_IntentBoostOff(t *testing.T) {
	now := time.Now().UTC()
	claims := []domain.Claim{
		{ID: "cl_fact", Text: "database choice", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, CreatedAt: now},
		{ID: "cl_dec", Text: "database choice", Type: domain.ClaimTypeDecision, Status: domain.ClaimStatusActive, CreatedAt: now},
	}
	e := NewEngine(fakeEventRepo{events: []domain.Event{{ID: "ev1", Content: "database choice", Timestamp: now}}},
		fakeClaimRepo{claims: claims}, fakeRelationshipRepo{})
	q := "what decision did we make on the database"

	if got := recallIDs(t, e, q, AnswerOptions{}); got[0] != "cl_dec" {
		t.Fatalf("shipped recall should put the decision first, got %v", got)
	}
	if got := recallIDs(t, e, q, AnswerOptions{Ablate: Ablation{IntentBoost: true}}); got[0] != "cl_fact" {
		t.Fatalf("with the intent boost off the input order should stand, got %v", got)
	}
}

// Salience is on by default through WithCognitiveDefaults; the ablation must
// win over it, or the leave-one-out row would silently measure the default.
func TestAblation_SalienceOverridesTheCognitiveDefault(t *testing.T) {
	t.Setenv(envSalience, "")
	now := time.Now().UTC()
	low := salienceClaim("cl_low", 0.1)
	high := salienceClaim("cl_high", 0.95)
	for _, c := range []*domain.Claim{&low, &high} {
		c.Status, c.CreatedAt, c.Text = domain.ClaimStatusActive, now, "equal"
	}
	e := NewEngine(fakeEventRepo{events: []domain.Event{{ID: "ev1", Content: "equal", Timestamp: now}}},
		fakeClaimRepo{claims: []domain.Claim{low, high}}, fakeRelationshipRepo{}).
		WithTextSearch(nil, fakeTextSearcher{hits: []ports.TextHit{{ID: "cl_low", Score: 5}, {ID: "cl_high", Score: 5}}})

	if got := recallIDs(t, e, "equal", AnswerOptions{}); got[0] != "cl_high" {
		t.Fatalf("default recall applies salience, got %v", got)
	}
	if got := recallIDs(t, e, "equal", AnswerOptions{Ablate: Ablation{Salience: true}}); got[0] != "cl_low" {
		t.Fatalf("salience ablation should leave the tie in input order, got %v", got)
	}
}

func TestAblation_InhibitionPenaltyOff(t *testing.T) {
	suppressed := domain.Claim{ID: "cl_sup", Text: "x", ConfidenceComponents: map[string]float64{domain.InhibitionComponentKey: 1}}
	plain := domain.Claim{ID: "cl_plain", Text: "x"}
	claims := []domain.Claim{suppressed, plain}
	e := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{}).
		WithTextSearch(nil, fakeTextSearcher{hits: []ports.TextHit{{ID: "cl_sup", Score: 5}, {ID: "cl_plain", Score: 5}}})
	ctx := context.Background()

	if got := e.rankClaimsByHybrid(ctx, "x", claims, false); got[0].ID != "cl_plain" {
		t.Fatalf("shipped ranking should sink the suppressed claim, got %v", got[0].ID)
	}
	if got := e.ablated(Ablation{Inhibition: true}).rankClaimsByHybrid(ctx, "x", claims, false); got[0].ID != "cl_sup" {
		t.Fatalf("with the penalty off the tie should keep input order, got %v", got[0].ID)
	}
}

// stubQueryEmbedder satisfies embedding.Client for wiring-only tests.
type stubQueryEmbedder struct{}

func (stubQueryEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1}
	}
	return out, nil
}
