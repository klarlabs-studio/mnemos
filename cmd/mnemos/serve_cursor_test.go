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

// The episode, association and embedding browses issue next_cursor too, and
// following it returns every row once with a stable total.
func TestServe_BrowseCursorsOnEveryListEndpoint(t *testing.T) {
	conn := newServerTestStore_conn(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var claims []domain.Claim
	for i := 0; i < 5; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		if err := conn.Events.Append(ctx, domain.Event{ID: fmt.Sprintf("hev%d", i), RunID: "r", Content: "e", SchemaVersion: "v1",
			SourceInputID: fmt.Sprintf("hsrc%d", i), Timestamp: at, IngestedAt: at}); err != nil {
			t.Fatal(err)
		}
		claims = append(claims, domain.Claim{ID: fmt.Sprintf("hc%d", i), Text: "c", Type: domain.ClaimTypeFact,
			Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: at, ValidFrom: at})
	}
	if err := conn.Claims.Upsert(ctx, claims); err != nil {
		t.Fatal(err)
	}
	var rels []domain.Relationship
	for i := 0; i < 4; i++ {
		rels = append(rels, domain.Relationship{ID: fmt.Sprintf("hrel%d", i), Type: domain.RelationshipTypeSupports,
			FromClaimID: fmt.Sprintf("hc%d", i), ToClaimID: fmt.Sprintf("hc%d", i+1), CreatedAt: base})
	}
	if err := conn.Relationships.Upsert(ctx, rels); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := conn.Embeddings.Upsert(ctx, fmt.Sprintf("hc%d", i), "claim", []float32{1, 0}, "m", ""); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	for _, ep := range []struct{ path, list string }{
		{"/v1/episodes", "episodes"}, {"/v1/associations", "associations"}, {"/v1/embeddings", "embeddings"},
	} {
		seen := map[string]bool{}
		q := "limit=2"
		total := -1
		for pages := 0; pages < 10; pages++ {
			resp, err := http.Get(srv.URL + ep.path + "?" + q)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]json.RawMessage
			_ = json.NewDecoder(resp.Body).Decode(&body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s: status %d", ep.path, resp.StatusCode)
			}
			var items []map[string]any
			_ = json.Unmarshal(body[ep.list], &items)
			var tot int
			_ = json.Unmarshal(body["total"], &tot)
			if total >= 0 && tot != total {
				t.Fatalf("%s: total changed %d -> %d", ep.path, total, tot)
			}
			total = tot
			for _, it := range items {
				key := fmt.Sprint(it["id"], it["entity_id"])
				if seen[key] {
					t.Fatalf("%s: %s on two pages", ep.path, key)
				}
				seen[key] = true
			}
			var next string
			_ = json.Unmarshal(body["next_cursor"], &next)
			if next == "" {
				break
			}
			q = "limit=2&cursor=" + url.QueryEscape(next)
		}
		if total <= 2 || len(seen) != total {
			t.Fatalf("%s: walked %d of %d", ep.path, len(seen), total)
		}
		resp, err := http.Get(srv.URL + ep.path + "?cursor=bogus")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: malformed cursor status %d, want 400", ep.path, resp.StatusCode)
		}
	}
}
