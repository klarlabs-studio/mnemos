package eval

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/query"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
	"gopkg.in/yaml.v3"
)

// retrievalCase mirrors retrieval.yaml. Kept tiny on purpose — the
// suite measures ranker quality, not parsing or schema.
type retrievalCase struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
	Seeds       []struct {
		ClaimID string `yaml:"claim_id"`
		Text    string `yaml:"text"`
	} `yaml:"seeds"`
	Query       string `yaml:"query"`
	GoldClaimID string `yaml:"gold_claim_id"`
}

type retrievalFile struct {
	TestCases []retrievalCase `yaml:"test_cases"`
}

// stubSemantic is a deterministic stand-in for an embedding-cosine
// signal. It scores each claim's text against the query by Jaccard
// overlap of stemmed token sets. Real embeddings are not available
// in the test environment (no provider, no API key), and a real
// stub provides no signal worth measuring. Jaccard is uncorrelated-
// enough with BM25 that combining the two is a meaningful test of
// the hybrid combiner — exactly what we want to exercise here.
type stubSemantic struct {
	textByID map[string]string
}

func (s stubSemantic) SearchByText(_ context.Context, q string, _ int) ([]ports.TextHit, error) {
	qTokens := tokenSet(q)
	if len(qTokens) == 0 {
		return nil, nil
	}
	out := make([]ports.TextHit, 0, len(s.textByID))
	for id, text := range s.textByID {
		t := tokenSet(text)
		score := jaccard(qTokens, t)
		if score == 0 {
			continue
		}
		out = append(out, ports.TextHit{ID: id, Score: score})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

func tokenSet(s string) map[string]struct{} {
	parts := strings.Fields(strings.ToLower(strings.TrimSpace(s)))
	out := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		p = strings.Trim(p, ",.;:!?\"'()[]{}")
		if len(p) < 3 {
			continue
		}
		out[p] = struct{}{}
	}
	return out
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func loadRetrievalCases(t *testing.T) []retrievalCase {
	t.Helper()
	path := filepath.Join(".", "retrieval.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read retrieval.yaml: %v", err)
	}
	var f retrievalFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse retrieval.yaml: %v", err)
	}
	if len(f.TestCases) == 0 {
		t.Fatal("retrieval.yaml has no cases")
	}
	return f.TestCases
}

// seedCase materialises a retrievalCase into a fresh DB: one event
// per seed, one claim per seed, an evidence link between them.
// Returns a store.Conn so each strategy can build its engine.
func seedCase(t *testing.T, c retrievalCase) (*store.Conn, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, c.ID+".db")
	conn, err := store.Open(context.Background(), "sqlite://"+dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := time.Now().UTC()
	for _, s := range c.Seeds {
		eventID := "ev_" + s.ClaimID
		if err := conn.Events.Append(context.Background(), domain.Event{
			ID:            eventID,
			RunID:         "r",
			SchemaVersion: "v1",
			Content:       s.Text,
			SourceInputID: "src",
			Timestamp:     now,
			IngestedAt:    now,
		}); err != nil {
			t.Fatalf("seed event %s: %v", eventID, err)
		}
		if err := conn.Claims.Upsert(context.Background(), []domain.Claim{{
			ID:         s.ClaimID,
			Text:       s.Text,
			Type:       "fact",
			Confidence: 0.85,
			Status:     "active",
			CreatedAt:  now,
		}}); err != nil {
			t.Fatalf("seed claim %s: %v", s.ClaimID, err)
		}
		if err := conn.Claims.UpsertEvidence(context.Background(), []domain.ClaimEvidence{{
			ClaimID: s.ClaimID,
			EventID: eventID,
		}}); err != nil {
			t.Fatalf("seed evidence %s: %v", s.ClaimID, err)
		}
	}
	return conn, func() { _ = conn.Close() }
}

// rankOf returns the 1-based position of gold in claims, or 0 when
// gold is absent. Reciprocal rank is 1/rank, with 0 meaning "missed".
func rankOf(claims []domain.Claim, gold string) int {
	for i, c := range claims {
		if c.ID == gold {
			return i + 1
		}
	}
	return 0
}

// TestRetrievalQuality runs the retrieval suite under three ranking
// strategies and reports MRR per strategy. Hybrid (BM25 + stub
// semantic) is asserted to outperform — or at minimum tie — pure
// BM25 across the suite, validating that the v0.10 combiner is
// pulling its weight.
func TestRetrievalQuality(t *testing.T) {
	cases := loadRetrievalCases(t)

	type strategyResult struct {
		name string
		// per-case ranks (0 = miss); MRR computed from these.
		ranks []int
	}
	strategies := []strategyResult{
		{name: "token-overlap", ranks: make([]int, len(cases))},
		{name: "bm25-only", ranks: make([]int, len(cases))},
		{name: "hybrid", ranks: make([]int, len(cases))},
	}

	for i, c := range cases {
		conn, cleanup := seedCase(t, c)
		defer cleanup()

		eventTS, ok := conn.Events.(ports.TextSearcher)
		if !ok {
			t.Fatal("events repo does not implement TextSearcher")
		}
		claimTS, ok := conn.Claims.(ports.TextSearcher)
		if !ok {
			t.Fatal("claims repo does not implement TextSearcher")
		}

		// Strategy 1: token-overlap (legacy)
		legacy := query.NewEngine(conn.Events, conn.Claims, conn.Relationships)
		ans, err := legacy.Answer(context.Background(), c.Query)
		if err != nil {
			t.Fatalf("[%s] token-overlap answer: %v", c.ID, err)
		}
		strategies[0].ranks[i] = rankOf(ans.Claims, c.GoldClaimID)

		// Strategy 2: BM25-only
		bm25Only := query.NewEngine(conn.Events, conn.Claims, conn.Relationships).
			WithTextSearch(eventTS, claimTS)
		ans, err = bm25Only.Answer(context.Background(), c.Query)
		if err != nil {
			t.Fatalf("[%s] bm25 answer: %v", c.ID, err)
		}
		strategies[1].ranks[i] = rankOf(ans.Claims, c.GoldClaimID)

		// Strategy 3: hybrid (BM25 + stub semantic). Build the stub
		// from the seed corpus so the second signal is meaningful.
		textByID := make(map[string]string, len(c.Seeds))
		for _, s := range c.Seeds {
			textByID[s.ClaimID] = s.Text
		}
		stub := stubSemantic{textByID: textByID}
		hybrid := query.NewEngine(conn.Events, conn.Claims, conn.Relationships).
			WithTextSearch(eventTS, stub)
		ans, err = hybrid.Answer(context.Background(), c.Query)
		if err != nil {
			t.Fatalf("[%s] hybrid answer: %v", c.ID, err)
		}
		strategies[2].ranks[i] = rankOf(ans.Claims, c.GoldClaimID)
	}

	t.Log("\n=== RETRIEVAL QUALITY EVAL ===")
	t.Logf("Strategy           MRR     Top-1 hits   per-case ranks")
	t.Logf("--------           ---     ----------   --------------")
	mrrs := make(map[string]float64, len(strategies))
	for _, s := range strategies {
		mrr, top1 := summarize(s.ranks)
		mrrs[s.name] = mrr
		t.Logf("%-18s %.3f      %d / %d        %v",
			s.name, mrr, top1, len(cases), s.ranks)
	}

	// Quality bar: BM25 should beat (or at least tie) token-overlap;
	// hybrid should beat (or tie) BM25. Loud regressions break the
	// build; a small dip might happen across reorderings of the
	// suite, so we use >= rather than strict >.
	if mrrs["bm25-only"]+1e-9 < mrrs["token-overlap"] {
		t.Fatalf("regression: BM25-only MRR %.3f < token-overlap MRR %.3f",
			mrrs["bm25-only"], mrrs["token-overlap"])
	}
	if mrrs["hybrid"]+1e-9 < mrrs["bm25-only"] {
		t.Fatalf("regression: hybrid MRR %.3f < BM25-only MRR %.3f",
			mrrs["hybrid"], mrrs["bm25-only"])
	}
}

// summarize returns Mean Reciprocal Rank and the count of cases
// where the gold claim was at rank 1.
func summarize(ranks []int) (float64, int) {
	var sum float64
	top1 := 0
	for _, r := range ranks {
		if r == 0 {
			continue
		}
		sum += 1.0 / float64(r)
		if r == 1 {
			top1++
		}
	}
	if len(ranks) == 0 {
		return 0, 0
	}
	return sum / float64(len(ranks)), top1
}
