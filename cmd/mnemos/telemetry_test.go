package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestInstallID_StableAcrossCalls(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	first, err := installID()
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := installID()
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if first != second {
		t.Errorf("install_id must be stable: first=%q second=%q", first, second)
	}
	if len(first) < 16 {
		t.Errorf("install_id must be ≥16 chars; got %q", first)
	}
}

func TestInstallID_RegeneratesAfterDelete(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	first, err := installID()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := os.Remove(filepath.Join(xdg, "mnemos", installIDFile)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	second, err := installID()
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first == second {
		t.Errorf("expected new install_id after delete; got same %q", first)
	}
}

func TestTelemetryOptIn_FileMarkerRoundTrip(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(envTelemetryOptIn, "")
	if telemetryOptIn() {
		t.Fatal("default state must be opt-out")
	}
	if err := setTelemetryOptIn(true); err != nil {
		t.Fatalf("set true: %v", err)
	}
	if !telemetryOptIn() {
		t.Fatal("after set true: must be opted in")
	}
	if err := setTelemetryOptIn(false); err != nil {
		t.Fatalf("set false: %v", err)
	}
	if telemetryOptIn() {
		t.Fatal("after set false: must be opted out")
	}
}

func TestTelemetryOptIn_EnvOverrideTakesPrecedence(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(envTelemetryOptIn, "true")
	if !telemetryOptIn() {
		t.Fatal("env opt-in must enable telemetry")
	}
	t.Setenv(envTelemetryOptIn, "no")
	if telemetryOptIn() {
		t.Fatal("env opt-in=no must NOT enable telemetry")
	}
}

func TestSendTelemetry_NoEndpointIsNoop(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(envTelemetryEndpoint, "")
	m := WorkspaceMetrics{TelemetryOptIn: true}
	sent, err := sendTelemetry(context.Background(), m)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if sent {
		t.Fatal("must not send when endpoint unconfigured")
	}
}

func TestSendTelemetry_OptOutIsNoop(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(envTelemetryEndpoint, "https://example.invalid/v1/telemetry")
	m := WorkspaceMetrics{TelemetryOptIn: false}
	sent, err := sendTelemetry(context.Background(), m)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if sent {
		t.Fatal("must not send when opted out")
	}
}

func TestSendTelemetry_PostsJSONWhenBothGatesOpen(t *testing.T) {
	var got WorkspaceMetrics
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want application/json", ct)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(envTelemetryEndpoint, srv.URL)

	m := WorkspaceMetrics{
		InstallID:                   "deadbeef",
		WorkspaceFingerprint:        "wf-abc",
		TelemetryOptIn:              true,
		TelemetryEndpointConfigured: true,
		ActiveRunIDs7d:              3,
	}
	sent, err := sendTelemetry(context.Background(), m)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !sent {
		t.Fatal("must send when both gates open")
	}
	if got.InstallID != "deadbeef" || got.ActiveRunIDs7d != 3 {
		t.Errorf("server received unexpected payload: %+v", got)
	}
}

func TestSendTelemetry_ServerErrorBubblesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(envTelemetryEndpoint, srv.URL)

	m := WorkspaceMetrics{TelemetryOptIn: true}
	sent, err := sendTelemetry(context.Background(), m)
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
	if sent {
		t.Fatal("must not report sent on 5xx")
	}
}

func TestComputeWorkspaceMetrics_Aggregates(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(xdg, "mnemos.db"))

	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	defer closeConn(conn)

	now := time.Now().UTC()
	recent := now.Add(-2 * 24 * time.Hour)
	old := now.Add(-30 * 24 * time.Hour)
	for _, e := range []domain.Event{
		{ID: "e1", RunID: "run-fresh", Content: "x", SourceInputID: "src-1", Timestamp: recent, IngestedAt: recent},
		{ID: "e2", RunID: "run-fresh", Content: "y", SourceInputID: "src-1", Timestamp: recent, IngestedAt: recent},
		{ID: "e3", RunID: "run-old", Content: "z", SourceInputID: "src-2", Timestamp: old, IngestedAt: old},
	} {
		if err := conn.Events.Append(ctx, e); err != nil {
			t.Fatalf("append event %s: %v", e.ID, err)
		}
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{
		{ID: "c1", Text: "fresh evidence-backed", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive,
			Confidence: 0.8, TrustScore: 0.7, CitationCount: 3, CreatedAt: recent},
		{ID: "c2", Text: "fresh no citations", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive,
			Confidence: 0.6, TrustScore: 0.5, CreatedAt: recent},
		{ID: "c3", Text: "old", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive,
			Confidence: 0.5, TrustScore: 0.4, CreatedAt: old},
	}); err != nil {
		t.Fatalf("upsert claims: %v", err)
	}

	m, err := computeWorkspaceMetrics(ctx, conn)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}

	if m.ActiveRunIDs7d != 1 {
		t.Errorf("active runs (7d) = %d, want 1 (run-fresh)", m.ActiveRunIDs7d)
	}
	if m.Events7d != 2 {
		t.Errorf("events (7d) = %d, want 2", m.Events7d)
	}
	if m.Claims7d != 2 {
		t.Errorf("claims (7d) = %d, want 2", m.Claims7d)
	}
	if m.EvidenceBackedClaims7d != 1 {
		t.Errorf("evidence-backed (7d) = %d, want 1 (c1)", m.EvidenceBackedClaims7d)
	}
	if !m.WeeklyActive {
		t.Errorf("weekly_active should be true (1 run + 1 evidence-backed)")
	}
	if m.TotalClaims != 3 {
		t.Errorf("total_claims = %d, want 3", m.TotalClaims)
	}
	if m.InstallID == "" {
		t.Error("install_id must be populated")
	}
	if m.WorkspaceFingerprint == "" {
		t.Error("workspace_fingerprint must be populated")
	}
}
