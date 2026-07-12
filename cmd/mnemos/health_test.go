package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestHealth_DefaultIsShallow(t *testing.T) {
	_, conn := openTestStore(t)
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want ok", body.Status)
	}
	if len(body.Checks) != 0 {
		t.Errorf("shallow check should not return per-subsystem checks: %+v", body.Checks)
	}
}

func TestHealth_DeepIncludesDBProbe(t *testing.T) {
	// LLM/embedding env unset → those probes return "skipped",
	// not "failed", so overall health stays ok.
	t.Setenv("MNEMOS_LLM_PROVIDER", "")
	t.Setenv("MNEMOS_EMBED_PROVIDER", "")

	_, conn := openTestStore(t)
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	// The deep subsystem report now lives behind auth at /internal/ready
	// (newServerMux runs with public reads so anonymous GET reaches it here).
	resp, err := http.Get(srv.URL + "/internal/ready")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Checks) == 0 {
		t.Fatal("deep check should return per-subsystem results")
	}
	foundDB := false
	for _, c := range body.Checks {
		if c.Name == "sqlite" {
			foundDB = true
			if c.Status != "ok" {
				t.Errorf("sqlite probe failed: %+v", c)
			}
		}
	}
	if !foundDB {
		t.Errorf("expected sqlite probe in checks: %+v", body.Checks)
	}
}

func TestRunHealthChecks_DBFailureReportsAsFailed(t *testing.T) {
	db, _ := openTestStore(t)
	// Close the DB so the probe transaction fails.
	_ = db.Close()

	r := runHealthChecks(context.Background(), db)
	if r.Healthy {
		t.Error("expected Healthy=false when DB write probe fails")
	}
	var dbCheck *healthCheck
	for i := range r.Checks {
		if r.Checks[i].Name == "sqlite" {
			dbCheck = &r.Checks[i]
		}
	}
	if dbCheck == nil || dbCheck.Status != "failed" {
		t.Errorf("expected sqlite check failed, got %+v", dbCheck)
	}
}

func TestRunDoctorChecks_NoCrashOnEmptyConfig(t *testing.T) {
	t.Setenv("MNEMOS_LLM_PROVIDER", "")
	t.Setenv("MNEMOS_EMBED_PROVIDER", "")
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "doctor.db"))

	r := runDoctorChecks(context.Background())
	// Healthy may be true or false depending on environment (Ollama
	// running etc.), but the call must succeed and emit checks.
	if len(r.Checks) == 0 {
		t.Fatal("expected at least one check")
	}
}
