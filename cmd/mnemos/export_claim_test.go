package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/markdown"
	"go.klarlabs.de/mnemos/internal/query"
)

// ---------------------------------------------------------------------------
// markdown.ExportClaim unit tests
// ---------------------------------------------------------------------------

func TestExportClaim_BasicFields(t *testing.T) {
	c := domain.Claim{
		ID:         "cl_export_1",
		Text:       "Deployment uses blue-green strategy.",
		Type:       domain.ClaimTypeFact,
		Status:     domain.ClaimStatusActive,
		Confidence: 0.9,
		TrustScore: 0.85,
		CreatedBy:  "agent-test",
		CreatedAt:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	out, err := markdown.ExportClaim(c, nil)
	if err != nil {
		t.Fatalf("ExportClaim: %v", err)
	}
	for _, want := range []string{
		"cl_export_1",
		"claim",
		"Deployment uses blue-green strategy.",
		"# Claim",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ExportClaim output missing %q\ngot:\n%s", want, out)
		}
	}
	// No provenance section when report is nil.
	if strings.Contains(out, "## Provenance") {
		t.Errorf("ExportClaim should not include Provenance section when report is nil")
	}
}

func TestExportClaim_WithProvenance(t *testing.T) {
	c := domain.Claim{
		ID:        "cl_export_2",
		Text:      "Cache hit-rate exceeds 90%%.",
		Type:      domain.ClaimTypeHypothesis,
		CreatedAt: time.Now().UTC(),
	}
	report := &domain.ProvenanceReport{
		ClaimID:   c.ID,
		Score:     0.72,
		Rationale: "high confidence, recent verification",
		Signals: []domain.ProvenanceSignal{
			{Name: "recency", Value: 0.9, Weight: 0.3, Contribution: 0.27},
			{Name: "confidence", Value: 0.75, Weight: 0.4, Contribution: 0.30},
		},
	}
	out, err := markdown.ExportClaim(c, report)
	if err != nil {
		t.Fatalf("ExportClaim with provenance: %v", err)
	}
	for _, want := range []string{
		"## Provenance",
		"0.72",
		"high confidence, recent verification",
		"recency",
		"confidence",
		"| Signal |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ExportClaim provenance output missing %q\ngot:\n%s", want, out)
		}
	}
}

// ---------------------------------------------------------------------------
// handleExportClaim unit test (uses live SQLite via openTestStore)
// ---------------------------------------------------------------------------

func TestHandleExportClaim_NotFound(t *testing.T) {
	_, conn := openTestStore(t)
	ctx := context.Background()
	_, err := handleExportClaim(ctx, conn, "non_existent_id", false)
	if err == nil {
		t.Fatal("expected error for missing claim, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestHandleExportClaim_Found(t *testing.T) {
	_, conn := openTestStore(t)
	ctx := context.Background()

	c := domain.Claim{
		ID:         "cl_exp_found_1",
		Text:       "Service mesh handles retries automatically.",
		Type:       domain.ClaimTypeFact,
		Status:     domain.ClaimStatusActive,
		Confidence: 0.88,
		TrustScore: 0.80,
		CreatedAt:  time.Now().UTC(),
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{c}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	out, err := handleExportClaim(ctx, conn, c.ID, false)
	if err != nil {
		t.Fatalf("handleExportClaim: %v", err)
	}
	if !strings.Contains(out, c.ID) {
		t.Errorf("output missing claim ID %q\ngot:\n%s", c.ID, out)
	}
	if !strings.Contains(out, c.Text) {
		t.Errorf("output missing claim text\ngot:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// EscalateClaimForAgent unit test
// ---------------------------------------------------------------------------

func TestEscalateClaimForAgent_NotFound(t *testing.T) {
	_, conn := openTestStore(t)
	ctx := context.Background()
	eng := query.NewEngine(conn.Events, conn.Claims, conn.Relationships)
	_, err := eng.EscalateClaimForAgent(ctx, "ghost_claim_id", "some reason")
	if err == nil {
		t.Fatal("expected error for non-existent claim, got nil")
	}
}

func TestEscalateClaimForAgent_ProducesEscalateVerdict(t *testing.T) {
	_, conn := openTestStore(t)
	ctx := context.Background()

	c := domain.Claim{
		ID:         "cl_escalate_1",
		Text:       "Database shards are balanced.",
		Type:       domain.ClaimTypeFact,
		Status:     domain.ClaimStatusActive,
		Confidence: 0.65,
		TrustScore: 0.60,
		CreatedAt:  time.Now().UTC(),
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{c}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	eng := query.NewEngine(conn.Events, conn.Claims, conn.Relationships)
	verdict, err := eng.EscalateClaimForAgent(ctx, c.ID, "contradictory evidence from monitoring")
	if err != nil {
		t.Fatalf("EscalateClaimForAgent: %v", err)
	}
	if verdict.Action != domain.VerdictActionEscalate {
		t.Errorf("expected Action=escalate, got %q", verdict.Action)
	}
	if verdict.WinnerClaimID != c.ID {
		t.Errorf("expected WinnerClaimID=%q, got %q", c.ID, verdict.WinnerClaimID)
	}
	if !strings.Contains(verdict.EscalationReason, "contradictory evidence") {
		t.Errorf("EscalationReason missing agent reason, got: %q", verdict.EscalationReason)
	}
}

func TestEscalateClaimForAgent_DefaultReason(t *testing.T) {
	_, conn := openTestStore(t)
	ctx := context.Background()

	c := domain.Claim{
		ID:        "cl_escalate_2",
		Text:      "Latency p99 is under 200ms.",
		Type:      domain.ClaimTypeHypothesis,
		Status:    domain.ClaimStatusActive,
		CreatedAt: time.Now().UTC(),
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{c}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	eng := query.NewEngine(conn.Events, conn.Claims, conn.Relationships)
	verdict, err := eng.EscalateClaimForAgent(ctx, c.ID, "")
	if err != nil {
		t.Fatalf("EscalateClaimForAgent empty reason: %v", err)
	}
	if verdict.Action != domain.VerdictActionEscalate {
		t.Errorf("expected Action=escalate, got %q", verdict.Action)
	}
	if !strings.Contains(verdict.EscalationReason, "no reason provided") {
		t.Errorf("expected default reason text, got: %q", verdict.EscalationReason)
	}
}

// ---------------------------------------------------------------------------
// HTTP GET /v1/claims/:id/export.md
// ---------------------------------------------------------------------------

func TestServeClaimExportMD_NotFound(t *testing.T) {
	_, conn := openTestStore(t)
	h := makeClaimSubresourceHandler(conn, wrapTestWriter(t, conn), nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/claims/missing_claim/export.md", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestServeClaimExportMD_ReturnsMarkdown(t *testing.T) {
	_, conn := openTestStore(t)
	ctx := context.Background()

	c := domain.Claim{
		ID:         "cl_http_export_1",
		Text:       "Circuit breaker opens after 5 consecutive failures.",
		Type:       domain.ClaimTypeFact,
		Status:     domain.ClaimStatusActive,
		Confidence: 0.9,
		TrustScore: 0.85,
		CreatedAt:  time.Now().UTC(),
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{c}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	h := makeClaimSubresourceHandler(conn, wrapTestWriter(t, conn), nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/claims/cl_http_export_1/export.md", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("expected text/markdown Content-Type, got %q", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, c.ID) {
		t.Errorf("response missing claim ID\ngot:\n%s", body)
	}
	if !strings.Contains(body, c.Text) {
		t.Errorf("response missing claim text\ngot:\n%s", body)
	}
}

func TestServeClaimExportMD_MethodNotAllowed(t *testing.T) {
	_, conn := openTestStore(t)
	h := makeClaimSubresourceHandler(conn, wrapTestWriter(t, conn), nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/claims/any_id/export.md", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}
