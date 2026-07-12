package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/trust"
)

// seedCuriosityStore writes claims through the store and recomputes trust so the
// stored trust_score reflects each claim's confidence and freshness (the same
// path the CLI reads). Confidence differentiates trust: a high-confidence claim
// scores high, a low-confidence one low.
func seedCuriosityStore(t *testing.T, dsn string, claims []domain.Claim) {
	t.Helper()
	ctx := context.Background()
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if len(claims) == 0 {
		return
	}
	if err := conn.Claims.Upsert(ctx, claims); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	now := time.Now().UTC()
	scorer, ok := conn.Claims.(ports.TrustScorer)
	if !ok {
		t.Fatalf("claim repo is not a TrustScorer")
	}
	if _, err := scorer.RecomputeTrust(ctx, func(confidence float64, evidenceCount int, latestEvidence time.Time) float64 {
		return trust.Score(confidence, evidenceCount, latestEvidence, now)
	}); err != nil {
		t.Fatalf("recompute trust: %v", err)
	}
}

func decodeCuriosity(t *testing.T, out string) (map[string]any, []map[string]any) {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	rawQ, _ := env["queue"].([]any)
	queue := make([]map[string]any, 0, len(rawQ))
	for _, it := range rawQ {
		if m, ok := it.(map[string]any); ok {
			queue = append(queue, m)
		}
	}
	return env, queue
}

func TestHandleCuriosity_RanksUncertainStaleFirst(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "curiosity.db")
	dsn := "sqlite://" + dbPath
	t.Setenv("MNEMOS_DB_URL", dsn)
	t.Setenv("MNEMOS_LLM_PROVIDER", "")
	t.Setenv("MNEMOS_EMBED_PROVIDER", "")

	now := time.Now().UTC()
	seedCuriosityStore(t, dsn, []domain.Claim{
		{
			ID:         "strong",
			Text:       "well-corroborated fact",
			Type:       domain.ClaimTypeFact,
			Confidence: 0.95,
			TrustScore: 0.95,
			Status:     domain.ClaimStatusActive,
			CreatedAt:  now,
			ValidFrom:  now,
		},
		{
			ID:         "weak",
			Text:       "shaky old belief",
			Type:       domain.ClaimTypeFact,
			Confidence: 0.3,
			TrustScore: 0.15,
			Status:     domain.ClaimStatusActive,
			CreatedAt:  now.AddDate(0, 0, -200),
			ValidFrom:  now.AddDate(0, 0, -200),
		},
	})

	out := captureStdout(t, func() { handleCuriosity(nil, Flags{}) })
	env, queue := decodeCuriosity(t, out)

	if len(queue) == 0 {
		t.Fatalf("expected a non-empty acquisition queue, got: %s", out)
	}
	if queue[0]["belief_id"] != "weak" {
		t.Fatalf("expected the uncertain/stale belief ranked first, got %v", queue[0]["belief_id"])
	}
	// The well-corroborated fresh belief has nothing to acquire — it must not appear.
	for _, it := range queue {
		if it["belief_id"] == "strong" {
			t.Fatalf("well-corroborated belief should not be in the curiosity queue: %s", out)
		}
	}
	if _, ok := env["calibration"]; !ok {
		t.Fatalf("expected calibration context in the envelope, got: %s", out)
	}
}

func TestHandleCuriosity_EmptyStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "empty.db")
	dsn := "sqlite://" + dbPath
	t.Setenv("MNEMOS_DB_URL", dsn)
	t.Setenv("MNEMOS_LLM_PROVIDER", "")
	t.Setenv("MNEMOS_EMBED_PROVIDER", "")
	// Touch the store so it exists but has no claims.
	seedCuriosityStore(t, dsn, nil)

	out := captureStdout(t, func() { handleCuriosity(nil, Flags{}) })
	env, queue := decodeCuriosity(t, out)
	if len(queue) != 0 {
		t.Fatalf("empty store must yield an empty queue, got: %s", out)
	}
	if c, _ := env["count"].(float64); c != 0 {
		t.Fatalf("empty store must report count 0, got %v", env["count"])
	}
}

func TestHandleCuriosity_LimitRespected(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "limit.db")
	dsn := "sqlite://" + dbPath
	t.Setenv("MNEMOS_DB_URL", dsn)
	t.Setenv("MNEMOS_LLM_PROVIDER", "")
	t.Setenv("MNEMOS_EMBED_PROVIDER", "")

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -200)
	seedCuriosityStore(t, dsn, []domain.Claim{
		{ID: "a", Text: "a", Type: domain.ClaimTypeFact, Confidence: 0.2, TrustScore: 0.1, Status: domain.ClaimStatusActive, CreatedAt: old, ValidFrom: old},
		{ID: "b", Text: "b", Type: domain.ClaimTypeFact, Confidence: 0.2, TrustScore: 0.2, Status: domain.ClaimStatusActive, CreatedAt: old, ValidFrom: old},
		{ID: "c", Text: "c", Type: domain.ClaimTypeFact, Confidence: 0.2, TrustScore: 0.3, Status: domain.ClaimStatusActive, CreatedAt: old, ValidFrom: old},
	})

	out := captureStdout(t, func() { handleCuriosity([]string{"--limit", "2"}, Flags{}) })
	_, queue := decodeCuriosity(t, out)
	if len(queue) != 2 {
		t.Fatalf("--limit 2 must cap the queue at 2, got %d: %s", len(queue), out)
	}
}
