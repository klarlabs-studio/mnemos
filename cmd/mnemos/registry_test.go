package main

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
)

func TestResolveRegistry_FlagWinsOverEnv(t *testing.T) {
	t.Setenv("MNEMOS_REGISTRY_URL", "https://from-env")
	t.Setenv("MNEMOS_REGISTRY_TOKEN", "env-token")
	regURL, token, err := resolveRegistry("https://from-flag", "flag-token")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if regURL != "https://from-flag" || token != "flag-token" {
		t.Fatalf("got url=%q token=%q, want from-flag/flag-token", regURL, token)
	}
}

func TestResolveRegistry_EnvWinsOverConfigFile(t *testing.T) {
	t.Setenv("MNEMOS_REGISTRY_URL", "https://from-env")
	t.Setenv("MNEMOS_REGISTRY_TOKEN", "")
	// No project config to fall back to in this test process — but env still wins.
	regURL, _, err := resolveRegistry("", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if regURL != "https://from-env" {
		t.Fatalf("url = %q, want from-env", regURL)
	}
}

func TestResolveRegistry_ErrorsWhenNothingConfigured(t *testing.T) {
	t.Setenv("MNEMOS_REGISTRY_URL", "")
	t.Setenv("MNEMOS_REGISTRY_TOKEN", "")
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	if _, _, err := resolveRegistry("", ""); err == nil {
		t.Fatal("expected error when no source configures a URL")
	}
}

func TestResolveRegistry_TrimsTrailingSlash(t *testing.T) {
	t.Setenv("MNEMOS_REGISTRY_URL", "")
	regURL, _, err := resolveRegistry("https://example.com/", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if regURL != "https://example.com" {
		t.Fatalf("url = %q, want trimmed", regURL)
	}
}

func TestEventsToBatches_SplitsAtBoundary(t *testing.T) {
	events := make([]eventDTO, pushBatchSize+5)
	for i := range events {
		events[i] = eventDTO{ID: "e" + string(rune('A'+i%26))}
	}
	batches := eventsToBatches(events)
	if len(batches) != 2 {
		t.Fatalf("got %d batches, want 2", len(batches))
	}
	first, _ := batches[0]["episodes"].([]eventDTO)
	if len(first) != pushBatchSize {
		t.Errorf("first batch len = %d, want %d", len(first), pushBatchSize)
	}
	second, _ := batches[1]["episodes"].([]eventDTO)
	if len(second) != 5 {
		t.Errorf("second batch len = %d, want 5", len(second))
	}
}

func TestClaimsToBatches_AttachesEvidenceToFirstBatchOnly(t *testing.T) {
	claims := []claimDTO{{ID: "c1"}, {ID: "c2"}}
	evidence := []claimEvidenceItem{{ClaimID: "c1", EventID: "e1"}}
	batches := claimsToBatches(claims, evidence)
	if len(batches) != 1 {
		t.Fatalf("got %d batches, want 1", len(batches))
	}
	if _, ok := batches[0]["evidence"]; !ok {
		t.Error("first batch missing evidence")
	}
}

// newFakeRegistry sets up a fake registry server and returns a token
// authorized to write to it. The token is a real Mnemos JWT minted
// against the test JWT secret (pinned by TestMain), and a matching user
// row is seeded so the server's revocation lookup path stays consistent
// with production.
func newFakeRegistry(t *testing.T) (url string, client *http.Client, token string, closer func()) {
	t.Helper()
	_, regConn := openTestStore(t)

	// Mint a token signed with the same secret TestMain pinned. Using
	// the env-exposed secret avoids having to plumb bytes through two
	// helpers just for tests.
	secretHex := os.Getenv("MNEMOS_JWT_SECRET")
	secret, err := hex.DecodeString(secretHex)
	if err != nil {
		t.Fatalf("decode test secret: %v", err)
	}
	user := domain.User{
		ID: "usr_pushtest", Name: "push", Email: "push@test.local",
		Status: domain.UserStatusActive, CreatedAt: time.Now().UTC(),
	}
	if err := regConn.Users.Create(context.Background(), user); err != nil {
		t.Fatalf("seed registry user: %v", err)
	}
	jwt, _, err := auth.NewIssuer(secret).IssueUserToken(user, time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	srv := httptest.NewServer(newServerMux(regConn))
	closer = func() {
		srv.Close()
	}
	return srv.URL, srv.Client(), jwt, closer
}

func TestPushPull_RoundTripsAllResources(t *testing.T) {
	regURL, _, regToken, closeReg := newFakeRegistry(t)
	defer closeReg()

	// Local DB seeded with 1 event, 1 claim, 1 evidence link, 1 relationship.
	_, localConn := openTestStore(t)

	now := time.Now().UTC()
	seedEventConn(t, localConn, "ev_1", "r1", "Local fact about caching.", "in_1", `{"source":"file"}`, now)
	seedClaimConn(t, localConn, "cl_1", "Caching cuts latency by 40%.", "fact", "active", 0.85, now)
	seedClaimConn(t, localConn, "cl_2", "We chose Redis over Memcached.", "decision", "active", 0.9, now)
	if err := localConn.Claims.UpsertEvidence(context.Background(), []domain.ClaimEvidence{
		{ClaimID: "cl_1", EventID: "ev_1"},
	}); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
	seedRelationshipConn(t, localConn, "rel_1", "supports", "cl_2", "cl_1", now)

	ctx := context.Background()
	client := &http.Client{Timeout: 10 * time.Second}

	// === push ===
	events, _ := loadAllEventsForPush(ctx, localConn)
	claims, evidence, _ := loadAllClaimsForPush(ctx, localConn)
	rels, _ := loadAllRelationshipsForPush(ctx, localConn)

	if n, err := pushBatched(ctx, client, regURL+"/v1/episodes", regToken, "events", eventsToBatches(events)); err != nil || n != 1 {
		t.Fatalf("push events n=%d err=%v", n, err)
	}
	if n, err := pushBatched(ctx, client, regURL+"/v1/beliefs", regToken, "claims", claimsToBatches(claims, evidence)); err != nil || n != 2 {
		t.Fatalf("push claims n=%d err=%v", n, err)
	}
	if n, err := pushBatched(ctx, client, regURL+"/v1/associations", regToken, "relationships", relsToBatches(rels)); err != nil || n != 1 {
		t.Fatalf("push relationships n=%d err=%v", n, err)
	}

	// === pull (into a fresh local DB to verify round-trip) ===
	_, pullConn := openTestStore(t)

	pulledEvents, err := pullEvents(ctx, client, regURL, "")
	if err != nil {
		t.Fatalf("pull events: %v", err)
	}
	pulledClaims, pulledEvidence, err := pullClaims(ctx, client, regURL, "")
	if err != nil {
		t.Fatalf("pull claims: %v", err)
	}
	pulledRels, err := pullRelationships(ctx, client, regURL, "")
	if err != nil {
		t.Fatalf("pull relationships: %v", err)
	}

	if n, _ := persistPulledEvents(ctx, wrapTestWriter(t, pullConn), pulledEvents); n != 1 {
		t.Errorf("inserted events = %d, want 1", n)
	}
	if n, _ := persistPulledClaims(ctx, wrapTestWriter(t, pullConn), pulledClaims, pulledEvidence); n != 2 {
		t.Errorf("inserted claims = %d, want 2", n)
	}
	if n, _ := persistPulledRelationships(ctx, wrapTestWriter(t, pullConn), pulledRels); n != 1 {
		t.Errorf("inserted relationships = %d, want 1", n)
	}

	// Second pull is a no-op (idempotent).
	if n, _ := persistPulledEvents(ctx, wrapTestWriter(t, pullConn), pulledEvents); n != 0 {
		t.Errorf("second pull inserted events = %d, want 0 (idempotent)", n)
	}
}

// TestPushBatched_IdempotentOnRetry covers the F-Slice E reliability
// fix: pre-fix, pushing the same batch twice would fail with
// PRIMARY KEY constraint violation on events because CreateEvent
// was a plain INSERT. Now it's INSERT ... ON CONFLICT(id) DO NOTHING
// so retried pushes after a flaky network are safe — the second
// push silently merges into the first.
func TestPushBatched_IdempotentOnRetry(t *testing.T) {
	regURL, _, regToken, closeReg := newFakeRegistry(t)
	defer closeReg()

	ctx := context.Background()
	client := &http.Client{Timeout: 10 * time.Second}
	now := time.Now().UTC().Format(time.RFC3339)

	// First push.
	batch := []map[string]any{{
		"episodes": []eventDTO{{
			ID: "ev_idem", RunID: "r", SchemaVersion: "v1",
			Content: "x", SourceInputID: "in", Timestamp: now,
			Metadata: map[string]string{},
		}},
	}}
	if n, err := pushBatched(ctx, client, regURL+"/v1/episodes", regToken, "events", batch); err != nil || n != 1 {
		t.Fatalf("first push n=%d err=%v", n, err)
	}

	// Second push of the exact same batch must succeed (idempotent).
	if n, err := pushBatched(ctx, client, regURL+"/v1/episodes", regToken, "events", batch); err != nil || n != 1 {
		t.Fatalf("retry push n=%d err=%v (pre-fix this would fail with PK violation)", n, err)
	}
}

func TestPushBatched_FailsOnServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	client := srv.Client()
	_, err := pushBatched(context.Background(), client, srv.URL, "", "events",
		[]map[string]any{{"episodes": []eventDTO{{ID: "e"}}}})
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
}

func TestPushBatched_PassesAuthHeader(t *testing.T) {
	gotAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"accepted":1,"skipped":0}`))
	}))
	defer srv.Close()

	if _, err := pushBatched(context.Background(), srv.Client(), srv.URL, "topsecret", "events",
		[]map[string]any{{"episodes": []eventDTO{{ID: "e"}}}}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if gotAuth != "Bearer topsecret" {
		t.Fatalf("Authorization header = %q, want 'Bearer topsecret'", gotAuth)
	}
}

func TestPushPull_EmbeddingsRoundTripBitExact(t *testing.T) {
	regURL, _, regToken, closeReg := newFakeRegistry(t)
	defer closeReg()

	// Seed two embeddings — one event, one claim — with vectors that include
	// edge cases: zero, negative, tiny fraction, a value that requires the
	// full float32 mantissa for round-trip equality.
	_, localConn := openTestStore(t)
	now := time.Now().UTC()
	seedEventConn(t, localConn, "ev_emb", "r", "x", "in", `{}`, now)
	seedClaimConn(t, localConn, "cl_emb", "x", "fact", "active", 0.7, now)
	ctx := context.Background()
	v1 := []float32{0.0, -1.5, 0.123456789, 3.14159265, -0.0001}
	v2 := []float32{1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0}
	if err := localConn.Embeddings.Upsert(ctx, "ev_emb", "event", v1, "test-model", ""); err != nil {
		t.Fatalf("upsert v1: %v", err)
	}
	if err := localConn.Embeddings.Upsert(ctx, "cl_emb", "claim", v2, "test-model", ""); err != nil {
		t.Fatalf("upsert v2: %v", err)
	}

	// === push ===
	embeddings, err := loadAllEmbeddingsForPush(ctx, localConn)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(embeddings) != 2 {
		t.Fatalf("loaded %d embeddings, want 2", len(embeddings))
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if n, err := pushBatched(ctx, client, regURL+"/v1/embeddings", regToken, "embeddings", embeddingsToBatches(embeddings)); err != nil || n != 2 {
		t.Fatalf("push n=%d err=%v", n, err)
	}

	// === pull into a fresh DB ===
	_, pullConn := openTestStore(t)

	pulled, err := pullEmbeddings(ctx, client, regURL, "")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(pulled) != 2 {
		t.Fatalf("pulled %d, want 2", len(pulled))
	}
	if n, err := persistPulledEmbeddings(ctx, wrapTestWriter(t, pullConn), pulled); err != nil || n != 2 {
		t.Fatalf("persist n=%d err=%v", n, err)
	}

	// === verify bit-exact round trip via the pull DB ===
	all, err := pullConn.Embeddings.ListAll(ctx)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	var gotV1, gotV2 domain.EmbeddingRecord
	for _, e := range all {
		if e.EntityID == "ev_emb" && e.EntityType == "event" {
			gotV1 = e
		}
		if e.EntityID == "cl_emb" && e.EntityType == "claim" {
			gotV2 = e
		}
	}
	if !equalFloat32Slice(gotV1.Vector, v1) {
		t.Errorf("v1 round-trip mismatch:\n  got:  %v\n  want: %v", gotV1.Vector, v1)
	}
	if !equalFloat32Slice(gotV2.Vector, v2) {
		t.Errorf("v2 round-trip mismatch:\n  got:  %v\n  want: %v", gotV2.Vector, v2)
	}
	if gotV1.Model != "test-model" {
		t.Errorf("model lost in transit: got %q", gotV1.Model)
	}
}

func TestAppendEmbeddings_RejectsLengthMismatch(t *testing.T) {
	regURL, _, regToken, closeReg := newFakeRegistry(t)
	defer closeReg()

	body := map[string]any{
		"embeddings": []map[string]any{{
			"entity_id": "x", "entity_type": "event",
			"vector": []float32{1.0, 2.0}, "model": "m", "dimensions": 5,
		}},
	}
	resp := postJSON(t, regURL+"/v1/embeddings", body, map[string]string{"Authorization": "Bearer " + regToken})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAppendEmbeddings_RejectsBadEntityType(t *testing.T) {
	regURL, _, regToken, closeReg := newFakeRegistry(t)
	defer closeReg()

	body := map[string]any{
		"embeddings": []map[string]any{{
			"entity_id": "x", "entity_type": "relationship",
			"vector": []float32{1.0}, "model": "m",
		}},
	}
	resp := postJSON(t, regURL+"/v1/embeddings", body, map[string]string{"Authorization": "Bearer " + regToken})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func equalFloat32Slice(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStampPullProvenance_AddsRegistryAndTimestamp(t *testing.T) {
	at := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	events := []eventDTO{
		{ID: "e1", Metadata: map[string]string{"source": "file"}},
		{ID: "e2"}, // nil metadata — must be initialized
	}
	stampPullProvenance(events, "https://reg.example.com", at)

	if events[0].Metadata["pulled_from_registry"] != "https://reg.example.com" {
		t.Errorf("e1 missing pulled_from_registry: %+v", events[0].Metadata)
	}
	if events[0].Metadata["source"] != "file" {
		t.Errorf("e1 lost original metadata: %+v", events[0].Metadata)
	}
	if events[1].Metadata == nil || events[1].Metadata["pulled_from_registry"] != "https://reg.example.com" {
		t.Errorf("e2 missing provenance: %+v", events[1].Metadata)
	}
	wantStamp := at.Format(time.RFC3339)
	if events[0].Metadata["pulled_at"] != wantStamp {
		t.Errorf("pulled_at = %q, want %q", events[0].Metadata["pulled_at"], wantStamp)
	}
}

func TestStampPullProvenance_DoesNotOverwriteExistingOrigin(t *testing.T) {
	events := []eventDTO{
		{ID: "e1", Metadata: map[string]string{
			"pulled_from_registry": "https://original.example.com",
			"pulled_at":            "2026-01-01T00:00:00Z",
		}},
	}
	stampPullProvenance(events, "https://newer.example.com", time.Now().UTC())
	if events[0].Metadata["pulled_from_registry"] != "https://original.example.com" {
		t.Errorf("origin overwritten: %q", events[0].Metadata["pulled_from_registry"])
	}
}

func TestPullEvents_PaginatesUntilExhausted(t *testing.T) {
	_, regConn := openTestStore(t)
	now := time.Now().UTC()
	for i := 0; i < pullPageSize+30; i++ {
		seedEventConn(t, regConn, "e"+string(rune('a'+i%26))+string(rune('0'+i/26)), "r", "x", "in"+string(rune('a'+i%26)), `{}`, now)
	}

	srv := httptest.NewServer(newServerMux(regConn))
	defer srv.Close()

	got, err := pullEvents(context.Background(), srv.Client(), srv.URL, "")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(got) != pullPageSize+30 {
		t.Fatalf("got %d events, want %d", len(got), pullPageSize+30)
	}
}
