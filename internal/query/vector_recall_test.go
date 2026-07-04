package query

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// fakeEmbedClient returns one fixed vector per input text, so the vector
// fast-path has something to search with.
type fakeEmbedClient struct{}

func (fakeEmbedClient) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}

// spyVectorRepo is an EmbeddingRepository that also implements
// EventVectorSearcher. It records whether the native search ran and returns
// the configured hits (or err, e.g. ErrVectorSearchUnavailable, to force a
// fallback). Only SearchEventsByVector is exercised; the rest are stubs.
type spyVectorRepo struct {
	hits           []ports.EventSimilarityHit
	err            error
	called         *int
	listByTypeCall *int
	gotModel       *string // records the model the engine passed to the searcher
}

func (r spyVectorRepo) SearchEventsByVector(_ context.Context, _ []float32, model string, _ int, _ float64) ([]ports.EventSimilarityHit, error) {
	*r.called++
	if r.gotModel != nil {
		*r.gotModel = model
	}
	if r.err != nil {
		return nil, r.err
	}
	return r.hits, nil
}

// modelAwareEmbedClient is a fakeEmbedClient that also reports a model id via
// embedding.ModelIdentifier, so a test can assert the engine threads it into
// the searcher.
type modelAwareEmbedClient struct {
	model string
}

func (c modelAwareEmbedClient) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}
func (c modelAwareEmbedClient) ModelID() string { return c.model }

func (spyVectorRepo) Upsert(context.Context, string, string, []float32, string, string) error {
	return nil
}
func (r spyVectorRepo) ListByEntityType(context.Context, string) ([]domain.EmbeddingRecord, error) {
	if r.listByTypeCall != nil {
		*r.listByTypeCall++
	}
	return nil, nil
}
func (spyVectorRepo) Delete(context.Context, string, string) error              { return nil }
func (spyVectorRepo) CountAll(context.Context) (int64, error)                   { return 0, nil }
func (spyVectorRepo) ListAll(context.Context) ([]domain.EmbeddingRecord, error) { return nil, nil }
func (spyVectorRepo) DeleteAll(context.Context) error                           { return nil }

// spyEventRepo records ListAll / ListByIDs calls so a test can prove which
// recall path the engine took. ListByIDs resolves ids against its map.
type spyEventRepo struct {
	byID         map[string]domain.Event
	all          []domain.Event
	listAllCount *int
	byIDsCount   *int
}

func (r spyEventRepo) Append(context.Context, domain.Event) error { return nil }
func (r spyEventRepo) GetByID(context.Context, string) (domain.Event, error) {
	return domain.Event{}, nil
}
func (r spyEventRepo) ListByIDs(_ context.Context, ids []string) ([]domain.Event, error) {
	*r.byIDsCount++
	out := make([]domain.Event, 0, len(ids))
	for _, id := range ids {
		if ev, ok := r.byID[id]; ok {
			out = append(out, ev)
		}
	}
	return out, nil
}
func (r spyEventRepo) ListAll(context.Context) ([]domain.Event, error) {
	*r.listAllCount++
	return r.all, nil
}
func (r spyEventRepo) CountAll(context.Context) (int64, error)  { return int64(len(r.all)), nil }
func (r spyEventRepo) DeleteByID(context.Context, string) error { return nil }
func (r spyEventRepo) DeleteAll(context.Context) error          { return nil }
func (r spyEventRepo) ListByRunID(context.Context, string) ([]domain.Event, error) {
	return nil, nil
}

// TestAnswer_VectorFastPath_SkipsListAll proves that when an EventVectorSearcher
// is wired, AnswerWithOptions serves recall from the top-K vector hits +
// ListByIDs and never falls back to loading the whole corpus with ListAll.
func TestAnswer_VectorFastPath_SkipsListAll(t *testing.T) {
	var searchCalls, listAllCalls, byIDsCalls, listByTypeCalls int
	target := domain.Event{ID: "e1", Content: "payments latency spike from a slow query"}
	events := spyEventRepo{
		byID:         map[string]domain.Event{"e1": target},
		all:          []domain.Event{target, {ID: "e2", Content: "unrelated"}},
		listAllCount: &listAllCalls,
		byIDsCount:   &byIDsCalls,
	}
	repo := spyVectorRepo{
		hits:           []ports.EventSimilarityHit{{EventID: "e1", Similarity: 0.9, Model: "test"}},
		called:         &searchCalls,
		listByTypeCall: &listByTypeCalls,
	}
	// A trusted, fresh claim so the fast-path answer grades as SUFFICIENT — the
	// corrective-retrieval gate (R3) must NOT fire, so the whole-corpus ListAll
	// path stays untouched. (An empty claim set would grade insufficient and
	// legitimately trigger the corrective widen — covered by its own test.)
	now := time.Now().UTC()
	claims := fakeClaimRepo{claims: []domain.Claim{{
		ID: "cl1", Text: "payments latency is caused by a slow query",
		TrustScore: 0.9, Confidence: 0.9, CreatedAt: now, ValidFrom: now,
	}}}
	engine := NewEngine(events, claims, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithEmbeddings(repo, fakeEmbedClient{})

	if _, err := engine.Answer("why is payments slow?"); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if searchCalls != 1 {
		t.Fatalf("expected native vector search to run once, got %d", searchCalls)
	}
	if byIDsCalls != 1 {
		t.Fatalf("expected ListByIDs to load the candidate events, got %d calls", byIDsCalls)
	}
	if listAllCalls != 0 {
		t.Fatalf("vector fast-path must NOT call ListAll, got %d calls", listAllCalls)
	}
	// The point of the fast-path: the native `<=>` ranking is used as-is. It
	// must NOT fall through to the Go-cosine re-rank, which reloads the whole
	// event-embedding corpus via ListByEntityType.
	if listByTypeCalls != 0 {
		t.Fatalf("fast-path must skip the whole-corpus cosine re-scan, got %d ListByEntityType calls", listByTypeCalls)
	}
}

// TestAnswer_FallsBackWhenVectorUnavailable proves the engine cleanly falls
// back to the ListAll + Go-cosine path when the searcher reports it has no
// native vector capability (ErrVectorSearchUnavailable).
func TestAnswer_FallsBackWhenVectorUnavailable(t *testing.T) {
	var searchCalls, listAllCalls, byIDsCalls int
	target := domain.Event{ID: "e1", Content: "payments latency spike from a slow query"}
	events := spyEventRepo{
		byID:         map[string]domain.Event{"e1": target},
		all:          []domain.Event{target},
		listAllCount: &listAllCalls,
		byIDsCount:   &byIDsCalls,
	}
	repo := spyVectorRepo{err: ports.ErrVectorSearchUnavailable, called: &searchCalls}
	engine := NewEngine(events, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithEmbeddings(repo, fakeEmbedClient{})

	if _, err := engine.Answer("why is payments slow?"); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if searchCalls != 1 {
		t.Fatalf("expected native vector search to be attempted once, got %d", searchCalls)
	}
	if listAllCalls != 1 {
		t.Fatalf("expected ListAll fallback exactly once, got %d", listAllCalls)
	}
}

// TestAnswer_ThreadsEmbedderModelToSearcher proves the engine passes the query
// embedder's model id (when it implements embedding.ModelIdentifier) down to
// the vector searcher, so recall filters to the current model space rather than
// comparing across models.
func TestAnswer_ThreadsEmbedderModelToSearcher(t *testing.T) {
	var searchCalls, listAllCalls, byIDsCalls int
	var gotModel string
	target := domain.Event{ID: "e1", Content: "payments latency spike from a slow query"}
	events := spyEventRepo{
		byID:         map[string]domain.Event{"e1": target},
		all:          []domain.Event{target},
		listAllCount: &listAllCalls,
		byIDsCount:   &byIDsCalls,
	}
	repo := spyVectorRepo{
		hits:     []ports.EventSimilarityHit{{EventID: "e1", Similarity: 0.9, Model: "voyage-3-large-256"}},
		called:   &searchCalls,
		gotModel: &gotModel,
	}
	engine := NewEngine(events, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithEmbeddings(repo, modelAwareEmbedClient{model: "voyage-3-large-256"})

	if _, err := engine.Answer("why is payments slow?"); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if searchCalls != 1 {
		t.Fatalf("expected native vector search to run once, got %d", searchCalls)
	}
	if gotModel != "voyage-3-large-256" {
		t.Fatalf("engine must thread the embedder model id to the searcher, got %q", gotModel)
	}
}
