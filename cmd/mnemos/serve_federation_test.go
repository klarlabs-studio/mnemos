package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// TestFederation_OptInRequired pins the load-bearing safety gate:
// without MNEMOS_FEDERATION_ENABLED=true the endpoint refuses to
// serve. A forgotten env flag must NOT silently leak playbooks.
func TestFederation_OptInRequired(t *testing.T) {
	_, conn := openTestStore(t)
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/federation/export")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 (disabled)", resp.StatusCode)
	}
}

// TestFederation_AnonymizesPlaybooks proves the projection drops
// every tenant-identifying field. Seeds a playbook with a known
// CreatedBy + DerivedFromLessons ids, then asserts those values do
// not appear anywhere in the response JSON.
func TestFederation_AnonymizesPlaybooks(t *testing.T) {
	t.Setenv("MNEMOS_FEDERATION_ENABLED", "true")
	_, conn := openTestStore(t)
	now := time.Now().UTC()
	if err := conn.Playbooks.Append(context.Background(), domain.Playbook{
		ID:         "pb_secret",
		Trigger:    "latency_spike_after_deploy",
		Statement:  "Roll back the last deploy",
		Confidence: 0.85,
		DerivedAt:  now,
		Source:     "synthesize",
		CreatedBy:  "usr_tenant_a_admin",
		Steps: []domain.PlaybookStep{
			{Order: 1, Action: "rollback", Description: "git revert HEAD~1"},
			{Order: 2, Action: "verify", Description: "p95 latency back to baseline"},
		},
	}); err != nil {
		t.Fatalf("seed playbook: %v", err)
	}
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/federation/export")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := json.Marshal(decodeFederationBody(t, resp))
	bodyStr := string(body)

	forbidden := []string{
		"pb_secret",          // the playbook id
		"usr_tenant_a_admin", // created_by
	}
	for _, leak := range forbidden {
		if strings.Contains(bodyStr, leak) {
			t.Errorf("federation export leaks %q in payload", leak)
		}
	}

	// But the value-carrying fields must survive.
	for _, must := range []string{
		"latency_spike_after_deploy", "Roll back the last deploy",
		"rollback", "verify",
	} {
		if !strings.Contains(bodyStr, must) {
			t.Errorf("federation export dropped expected field %q", must)
		}
	}
}

// TestFederation_PreservesLessonCount_NotIDs proves the lesson_count
// integer survives — that IS useful population-level signal — while
// the raw ids do not.
func TestFederation_PreservesLessonCount_NotIDs(t *testing.T) {
	in := []domain.Playbook{{
		Trigger: "x", Statement: "y", Confidence: 0.6, DerivedAt: time.Now().UTC(),
		DerivedFromLessons: []string{"a", "b", "c"},
	}}
	out := anonymizePlaybooks(in)
	if len(out) != 1 {
		t.Fatal()
	}
	if out[0].LessonCount != 3 {
		t.Errorf("lesson_count = %d, want 3", out[0].LessonCount)
	}
}

func decodeFederationBody(t *testing.T, resp *http.Response) federationExportResponse {
	t.Helper()
	var out federationExportResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}
