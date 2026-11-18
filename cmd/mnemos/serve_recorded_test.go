package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/store"
)

// ADR 0036 step 2: GET /v1/beliefs?recorded_as_of= returns the beliefs the
// brain held at T as it held them, with the evidence they stood on then: the
// same rewind recall applies. Before it the browse filtered today's rows on
// created_at, so a belief retracted after T came back deprecated and a belief
// merged after T came back as a redirect with no evidence.
func TestServe_ListBeliefsRecordedAsOfRewinds(t *testing.T) {
	_, conn := openTestStore(t)
	tuesday := seedRecordedTimeline(t, conn)

	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	q := url.Values{"recorded_as_of": {tuesday.Format(time.RFC3339Nano)}, "status": {"active"}}
	resp, err := http.Get(srv.URL + "/v1/beliefs?" + q.Encode())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body claimsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var ids []string
	for _, c := range body.Claims {
		ids = append(ids, c.ID)
		if c.Status != string(domain.ClaimStatusActive) {
			t.Errorf("%s reads as %q at T, want active", c.ID, c.Status)
		}
	}
	sort.Strings(ids)
	if want := []string{"cl-held", "cl-merged", "cl-retracted"}; !equalStrings(ids, want) {
		t.Fatalf("recorded_as_of browse = %v, want %v", ids, want)
	}
	evidence := map[string]string{}
	for _, e := range body.Evidence {
		evidence[e.ClaimID] = e.EventID
	}
	if evidence["cl-merged"] != "ev-merged" {
		t.Errorf("cl-merged's evidence at T = %q, want ev-merged (the merge moved it onto cl-winner)", evidence["cl-merged"])
	}
}

// seedRecordedTimeline records three beliefs, then after the returned instant
// retracts one, folds another into a restatement, and records a new one.
func seedRecordedTimeline(t *testing.T, conn *store.Conn) time.Time {
	t.Helper()
	ctx := context.Background()
	add := func(eventID, claimID, text string) {
		t.Helper()
		now := time.Now().UTC()
		if err := conn.Events.Append(ctx, domain.Event{
			ID: eventID, RunID: "timeline", SchemaVersion: "v1", SourceInputID: "timeline",
			Content: text, Timestamp: now, IngestedAt: now,
		}); err != nil {
			t.Fatalf("append %s: %v", eventID, err)
		}
		if err := conn.Claims.Upsert(ctx, []domain.Claim{{
			ID: claimID, Text: text, Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive,
			Confidence: 0.8, CreatedAt: now,
		}}); err != nil {
			t.Fatalf("upsert %s: %v", claimID, err)
		}
		if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: claimID, EventID: eventID}}); err != nil {
			t.Fatalf("evidence %s: %v", claimID, err)
		}
	}
	add("ev-held", "cl-held", "the payments pool size is 50")
	add("ev-retracted", "cl-retracted", "the outage was caused by disk saturation")
	add("ev-merged", "cl-merged", "deploys go out on tuesdays")
	time.Sleep(5 * time.Millisecond)
	tuesday := time.Now().UTC()
	time.Sleep(5 * time.Millisecond)

	retracted, err := conn.Claims.ListByIDs(ctx, []string{"cl-retracted"})
	if err != nil || len(retracted) != 1 {
		t.Fatalf("load cl-retracted: %v", err)
	}
	retracted[0].Status = domain.ClaimStatusDeprecated
	if err := conn.Claims.UpsertWithReason(ctx, retracted, "retracted"); err != nil {
		t.Fatalf("retract: %v", err)
	}
	add("ev-winner", "cl-winner", "deploys happen every tuesday")
	if _, err := pipeline.ApplySemanticDedupe(ctx, conn, pipeline.SemanticDedupePlan{
		Merges: []pipeline.SemanticMerge{{WinnerID: "cl-winner", DuplicateIDs: []string{"cl-merged"}}},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	add("ev-new", "cl-new", "the payments pool size is 80")
	return tuesday
}

func equalStrings(a, b []string) bool {
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
