package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/store/postgres"
)

func TestParseDSN_DefaultsNamespaceToMnemos(t *testing.T) {
	t.Parallel()
	d, err := postgres.ParseDSN("postgres://user:pw@host:5432/cogstack")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if d.Namespace != "mnemos" {
		t.Errorf("Namespace = %q, want mnemos (default)", d.Namespace)
	}
	// LibpqDSN should equal Raw when there was no namespace param to strip.
	if d.LibpqDSN != "postgres://user:pw@host:5432/cogstack" {
		t.Errorf("LibpqDSN = %q, want raw passthrough", d.LibpqDSN)
	}
}

func TestParseDSN_StripsNamespaceFromQuery(t *testing.T) {
	t.Parallel()
	d, err := postgres.ParseDSN("postgres://user:pw@host/cogstack?sslmode=require&namespace=team_x")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if d.Namespace != "team_x" {
		t.Errorf("Namespace = %q, want team_x", d.Namespace)
	}
	if strings.Contains(d.LibpqDSN, "namespace=") {
		t.Errorf("LibpqDSN should not contain namespace=, got %q", d.LibpqDSN)
	}
	if !strings.Contains(d.LibpqDSN, "sslmode=require") {
		t.Errorf("LibpqDSN dropped non-namespace params: %q", d.LibpqDSN)
	}
}

func TestParseDSN_AcceptsPostgresqlScheme(t *testing.T) {
	t.Parallel()
	d, err := postgres.ParseDSN("postgresql://user:pw@host/cogstack")
	if err != nil {
		t.Fatalf("ParseDSN(postgresql://...): %v", err)
	}
	if d.Namespace != "mnemos" {
		t.Errorf("Namespace = %q, want mnemos", d.Namespace)
	}
}

func TestParseDSN_RejectsInvalidNamespace(t *testing.T) {
	t.Parallel()
	bad := []string{
		"postgres://h/d?namespace=Team-X", // hyphen + capitals
		"postgres://h/d?namespace=1team",  // starts with digit
		"postgres://h/d?namespace=",       // empty after = → defaults; not invalid
		"postgres://h/d?namespace=very_very_long_name_that_exceeds_the_postgres_identifier_limit_of_63_chars",
	}
	for _, dsn := range bad {
		_, err := postgres.ParseDSN(dsn)
		if dsn == "postgres://h/d?namespace=" {
			// Empty defaults to mnemos; should NOT error.
			if err != nil {
				t.Errorf("empty namespace should default, got %v", err)
			}
			continue
		}
		if err == nil {
			t.Errorf("ParseDSN(%q) accepted invalid namespace", dsn)
		}
	}
}

func TestParseDSN_RejectsNonPostgresScheme(t *testing.T) {
	t.Parallel()
	if _, err := postgres.ParseDSN("sqlite:///x.db"); err == nil {
		t.Error("ParseDSN accepted sqlite:// scheme")
	}
}

// TestStoreOpen_BadDSN exercises the unhappy path: a syntactically-
// valid postgres:// DSN pointing nowhere should return a clear
// connection error rather than panic. We deliberately keep this
// fast (no real network round-trip — invalid host on a port that
// refuses connections immediately on most systems).
func TestStoreOpen_BadDSN(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Reserved IP that no listener responds on; ping should time out
	// or refuse fast on every platform. The point is to prove the
	// provider surfaces driver-level errors with the DSN context
	// rather than crashing.
	_, err := store.Open(ctx, "postgres://nobody:nopw@127.0.0.1:1/nodb?sslmode=disable")
	if err == nil {
		t.Fatal("expected error opening a bad postgres dsn, got nil")
	}
	// We don't pin the exact error chain — pgx's error surface
	// varies by environment. Just confirm something happened and
	// it's not the old scaffold sentinel.
	if errors.Is(err, postgres.ErrNotImplemented) {
		t.Errorf("provider still returning ErrNotImplemented: %v", err)
	}
}

func TestSupportedSchemes_IncludesPostgres(t *testing.T) {
	t.Parallel()
	got := store.SupportedSchemes()
	want := map[string]bool{"postgres": false, "postgresql": false}
	for _, s := range got {
		if _, ok := want[s]; ok {
			want[s] = true
		}
	}
	for s, seen := range want {
		if !seen {
			t.Errorf("SupportedSchemes missing %q (got %v)", s, got)
		}
	}
}

// A malformed DSN must never echo the password back in the error (redaction).
func TestParseDSNRedactsPasswordOnError(t *testing.T) {
	// A space in the host makes url.Parse fail, exercising the error path.
	_, err := postgres.ParseDSN("postgres://user:supersecret@ho st/db")
	if err == nil {
		t.Fatal("expected a parse error for a malformed DSN")
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Errorf("password leaked in error: %v", err)
	}
}
