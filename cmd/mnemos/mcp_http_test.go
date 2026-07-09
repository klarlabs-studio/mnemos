package main

import "testing"

func TestParseMCPArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantAddr string
		wantAuth bool
		wantErr  bool
	}{
		{name: "default stdio", args: nil, wantAddr: "", wantAuth: false},
		{name: "http requires auth by default", args: []string{"--http", ":8081"}, wantAddr: ":8081", wantAuth: true},
		{name: "http no-auth", args: []string{"--http", ":8081", "--no-auth"}, wantAddr: ":8081", wantAuth: false},
		{name: "http explicit auth", args: []string{"--http", "127.0.0.1:9", "--auth"}, wantAddr: "127.0.0.1:9", wantAuth: true},
		{name: "http missing addr", args: []string{"--http"}, wantErr: true},
		{name: "unknown flag", args: []string{"--nope"}, wantErr: true},
		// Auth flags are meaningless without --http and must not imply a listener.
		{name: "auth without http stays stdio", args: []string{"--auth"}, wantAddr: "", wantAuth: false},
		{name: "require-tenant implies auth", args: []string{"--http", ":8081", "--require-tenant"}, wantAddr: ":8081", wantAuth: true},
		{name: "require-tenant conflicts with no-auth", args: []string{"--http", ":8081", "--require-tenant", "--no-auth"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseMCPArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.httpAddr != tt.wantAddr || cfg.requireAuth != tt.wantAuth {
				t.Errorf("parseMCPArgs(%v) = {addr:%q auth:%v}, want {addr:%q auth:%v}",
					tt.args, cfg.httpAddr, cfg.requireAuth, tt.wantAddr, tt.wantAuth)
			}
		})
	}
}

func TestParseMCPArgsRequireTenant(t *testing.T) {
	cfg, err := parseMCPArgs([]string{"--http", ":9", "--require-tenant"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.requireTenant || !cfg.requireAuth {
		t.Errorf("require-tenant should imply auth: %+v", cfg)
	}
	// Without --http, tenancy is meaningless and must be off.
	cfg, _ = parseMCPArgs([]string{"--require-tenant"})
	if cfg.requireTenant {
		t.Error("require-tenant without --http must not enable tenancy")
	}
}

func TestBearerToken(t *testing.T) {
	cases := map[string]string{
		"Bearer abc123":   "abc123",
		"bearer abc123":   "abc123",
		"BEARER  spaced ": "spaced",
		"Basic xyz":       "",
		"":                "",
		"Bearer":          "",
		"abc123":          "",
	}
	for in, want := range cases {
		if got := bearerToken(in); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", in, got, want)
		}
	}
}
