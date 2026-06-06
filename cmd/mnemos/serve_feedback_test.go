package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// feedbackFixture mirrors the gdprFixture shape. Centralising the
// JWT + seeded user + httptest server keeps each feedback test body
// focused on the assertions that matter.
type feedbackFixture struct {
	conn   *store.Conn
	srv    *httptest.Server
	header string
}

func newFeedbackFixture(t *testing.T) feedbackFixture {
	t.Helper()
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 13)
	}
	t.Setenv("MNEMOS_JWT_SECRET", hex.EncodeToString(secret))
	t.Setenv("HOME", t.TempDir())

	_, conn := openTestStore(t)
	if err := conn.Users.Create(context.Background(), domain.User{
		ID:        "usr_reviewer",
		Name:      "reviewer",
		Email:     "r@test.local",
		Status:    domain.UserStatusActive,
		Scopes:    []string{domain.ScopeClaimsWrite},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	srv := httptest.NewServer(newServerMux(conn))
	t.Cleanup(srv.Close)

	tok, _, err := auth.NewIssuer(secret).IssueUserToken(domain.User{
		ID:     "usr_reviewer",
		Scopes: []string{domain.ScopeClaimsWrite},
		Status: domain.UserStatusActive,
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return feedbackFixture{conn: conn, srv: srv, header: "Bearer " + tok}
}

func postFeedback(t *testing.T, f feedbackFixture, claimID string, body feedbackRequest) (int, feedbackResponse) {
	t.Helper()
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/v1/claims/"+claimID+"/feedback", bytes.NewReader(buf))
	req.Header.Set("Authorization", f.header)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out feedbackResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func seedFeedbackClaim(t *testing.T, f feedbackFixture) string {
	t.Helper()
	id := fmt.Sprintf("cl_fb_%d", time.Now().UnixNano())
	seedClaimConn(t, f.conn, id, "claim", "fact", "active", 0.9, time.Now().UTC())
	return id
}

// TestFeedback_NegativeDecaysConfidence pins the load-bearing decay:
// each "not helpful" vote multiplies the scalar Confidence by the
// configured factor (default 0.9). Two votes in a row should compose.
func TestFeedback_NegativeDecaysConfidence(t *testing.T) {
	f := newFeedbackFixture(t)
	claimID := seedFeedbackClaim(t, f)

	status, resp := postFeedback(t, f, claimID, feedbackRequest{Helpful: false, Note: "stale"})
	if status != http.StatusOK {
		t.Fatalf("first feedback status = %d", status)
	}
	wantOne := 0.9 * 0.9
	if !approxEqual(resp.NewConfidence, wantOne, 1e-9) {
		t.Errorf("after 1 negative, confidence = %v, want ~%v", resp.NewConfidence, wantOne)
	}
	if resp.NegativeFeedbackStreak != 1 {
		t.Errorf("streak = %d, want 1", resp.NegativeFeedbackStreak)
	}

	_, resp = postFeedback(t, f, claimID, feedbackRequest{Helpful: false})
	wantTwo := 0.9 * 0.9 * 0.9
	if !approxEqual(resp.NewConfidence, wantTwo, 1e-9) {
		t.Errorf("after 2 negatives, confidence = %v, want ~%v", resp.NewConfidence, wantTwo)
	}
	if resp.NegativeFeedbackStreak != 2 {
		t.Errorf("streak = %d, want 2", resp.NegativeFeedbackStreak)
	}
}

// TestFeedback_AutoContestsAtThreshold pins the load-bearing
// behaviour the issue called out: after N consecutive negative
// corrections, status auto-transitions to "contested".
func TestFeedback_AutoContestsAtThreshold(t *testing.T) {
	f := newFeedbackFixture(t)
	t.Setenv("MNEMOS_FEEDBACK_CONTEST_THRESHOLD", "3")
	claimID := seedFeedbackClaim(t, f)

	for i := 0; i < 2; i++ {
		_, resp := postFeedback(t, f, claimID, feedbackRequest{Helpful: false})
		if resp.AutoContested {
			t.Fatalf("auto-contested after %d negatives, want 3", i+1)
		}
		if resp.NewStatus != "active" {
			t.Errorf("status flipped early after %d negatives: %q", i+1, resp.NewStatus)
		}
	}
	_, resp := postFeedback(t, f, claimID, feedbackRequest{Helpful: false})
	if !resp.AutoContested {
		t.Errorf("auto-contested flag missing on 3rd negative: %+v", resp)
	}
	if resp.NewStatus != "contested" {
		t.Errorf("status = %q, want contested", resp.NewStatus)
	}
}

// TestFeedback_PositiveResetsStreak proves a helpful vote zeroes
// the consecutive-negative counter so subsequent push-back has to
// climb again. Without this the auto-contest path would fire on
// stale state.
func TestFeedback_PositiveResetsStreak(t *testing.T) {
	f := newFeedbackFixture(t)
	t.Setenv("MNEMOS_FEEDBACK_CONTEST_THRESHOLD", "3")
	claimID := seedFeedbackClaim(t, f)

	postFeedback(t, f, claimID, feedbackRequest{Helpful: false})
	postFeedback(t, f, claimID, feedbackRequest{Helpful: false})
	_, resp := postFeedback(t, f, claimID, feedbackRequest{Helpful: true})
	if resp.NegativeFeedbackStreak != 0 {
		t.Errorf("streak not reset by helpful=true: %d", resp.NegativeFeedbackStreak)
	}
	if resp.HelpfulCount != 1 {
		t.Errorf("helpful_count = %d, want 1", resp.HelpfulCount)
	}
	if resp.NewStatus != "active" {
		t.Errorf("status changed on helpful: %q", resp.NewStatus)
	}
}

// TestFeedback_RequiresClaimsWriteScope blocks the call without
// claims:write. Feedback is a privileged write — public reads must
// not be able to decay confidence on someone else's claim.
func TestFeedback_RequiresClaimsWriteScope(t *testing.T) {
	f := newFeedbackFixture(t)
	claimID := seedFeedbackClaim(t, f)
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/v1/claims/"+claimID+"/feedback",
		bytes.NewReader([]byte(`{"helpful":true}`)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 400 {
		t.Errorf("status = %d, want 4xx (auth required)", resp.StatusCode)
	}
}

// TestFeedback_UnknownClaim returns 404 rather than a 200 with
// pseudo-state. A typo in the claim id must surface.
func TestFeedback_UnknownClaim(t *testing.T) {
	f := newFeedbackFixture(t)
	status, _ := postFeedback(t, f, "cl_nope", feedbackRequest{Helpful: true})
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", status)
	}
}

// TestFeedback_DecaysCorroborationComponent proves the component
// decomposition stays internally consistent: when the scalar
// Confidence decays, the "corroboration" component decays in
// lockstep. Other components stay untouched.
func TestFeedback_DecaysCorroborationComponent(t *testing.T) {
	f := newFeedbackFixture(t)
	claimID := "cl_components"
	if err := f.conn.Claims.Upsert(context.Background(), []domain.Claim{{
		ID:         claimID,
		Text:       "claim with components",
		Type:       domain.ClaimTypeFact,
		Confidence: 0.9,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  time.Now().UTC(),
		ConfidenceComponents: map[string]float64{
			"corroboration":    0.5,
			"source_authority": 0.8,
		},
	}}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	postFeedback(t, f, claimID, feedbackRequest{Helpful: false})

	got, _ := f.conn.Claims.ListByIDs(context.Background(), []string{claimID})
	if len(got) != 1 {
		t.Fatalf("claim not found after feedback")
	}
	want := 0.5 * 0.9
	if !approxEqual(got[0].ConfidenceComponents["corroboration"], want, 1e-9) {
		t.Errorf("corroboration = %v, want ~%v", got[0].ConfidenceComponents["corroboration"], want)
	}
	if got[0].ConfidenceComponents["source_authority"] != 0.8 {
		t.Errorf("source_authority decayed: %v (should stay 0.8)", got[0].ConfidenceComponents["source_authority"])
	}
}

func approxEqual(a, b, eps float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= eps
}
