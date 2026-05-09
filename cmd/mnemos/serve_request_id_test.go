package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestID_GeneratedWhenAbsent(t *testing.T) {
	srv := httptest.NewServer(newServerMux(newServerTestStore_conn(t)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got := resp.Header.Get("X-Request-ID")
	if got == "" {
		t.Fatal("missing X-Request-ID on response")
	}
	if len(got) != 32 { // 16 bytes hex
		t.Errorf("X-Request-ID length = %d, want 32 (16-byte hex)", len(got))
	}
}

func TestRequestID_PassedThroughWhenSupplied(t *testing.T) {
	srv := httptest.NewServer(newServerMux(newServerTestStore_conn(t)))
	defer srv.Close()

	const want = "abc-123_xyz"
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/health", nil)
	req.Header.Set("X-Request-ID", want)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Header.Get("X-Request-ID"); got != want {
		t.Errorf("X-Request-ID = %q, want %q (caller-supplied id must round-trip)", got, want)
	}
}

func TestRequestID_RejectsHostileInputAndMintsFresh(t *testing.T) {
	srv := httptest.NewServer(newServerMux(newServerTestStore_conn(t)))
	defer srv.Close()

	cases := []string{
		"id with spaces",
		"id+with+plus",
		"id;with;semicolon",
		strings.Repeat("a", 65), // > 64 char cap
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/health", nil)
			req.Header.Set("X-Request-ID", raw)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			got := resp.Header.Get("X-Request-ID")
			if got == raw {
				t.Errorf("hostile input %q passed through; want minted replacement", raw)
			}
			if got == "" {
				t.Errorf("hostile input %q produced empty id", raw)
			}
		})
	}
}
