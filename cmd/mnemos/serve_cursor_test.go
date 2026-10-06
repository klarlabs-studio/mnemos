package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// GET /v1/beliefs issues next_cursor while more remain, following it walks
// every belief once, and a malformed cursor or a cursor combined with offset
// is a 400 rather than a silent first page.
func TestServe_BeliefsCursorPagination(t *testing.T) {
	conn := newServerTestStore_conn(t)
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var claims []domain.Claim
	for i := 0; i < 7; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		claims = append(claims, domain.Claim{ID: fmt.Sprintf("p%d", i), Text: "belief", Type: domain.ClaimTypeFact,
			Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: at, ValidFrom: at})
	}
	if err := conn.Claims.Upsert(context.Background(), claims); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	get := func(q string) (int, claimsResponse) {
		resp, err := http.Get(srv.URL + "/v1/beliefs?" + q)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body claimsResponse
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}

	seen := map[string]bool{}
	q := "limit=3"
	for pages := 0; ; pages++ {
		code, body := get(q)
		if code != http.StatusOK {
			t.Fatalf("page %d: status %d", pages, code)
		}
		if body.Total != 7 {
			t.Fatalf("page %d: total %d, want 7", pages, body.Total)
		}
		for _, c := range body.Claims {
			if seen[c.ID] {
				t.Fatalf("belief %s on two pages", c.ID)
			}
			seen[c.ID] = true
		}
		if body.NextCursor == "" {
			break
		}
		if pages > 5 {
			t.Fatal("next_cursor never ends")
		}
		q = "limit=3&cursor=" + url.QueryEscape(body.NextCursor)
	}
	if len(seen) != 7 {
		t.Fatalf("walked %d beliefs, want 7", len(seen))
	}
	if code, _ := get("cursor=not-a-cursor"); code != http.StatusBadRequest {
		t.Errorf("malformed cursor: status %d, want 400", code)
	}
	_, first := get("limit=3")
	if code, _ := get("offset=3&cursor=" + url.QueryEscape(first.NextCursor)); code != http.StatusBadRequest {
		t.Errorf("cursor with offset: status %d, want 400", code)
	}
}
