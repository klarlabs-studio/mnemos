package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/store"

	_ "go.klarlabs.de/mnemos/internal/store/libsql"
)

// leakCanary is a credential no real DSN carries, so finding it in output can
// only mean the credential was echoed.
const leakCanary = "LEAKCANARYx9Qz7"

// Opening a store with credentials it cannot use must fail WITHOUT echoing the
// credentials. Every provider puts the DSN into its error for diagnosis, and
// each did it differently: MySQL printed the driver form "user:pw@tcp(...)" raw
// in its ping error and wrapped *url.Error (whose text quotes the raw URL) on
// parse; libSQL redacted only the exact, case-sensitive "authToken="; the
// shared RedactDSN knew only "://user:pw@". These errors reach `mnemos doctor`,
// `serve` startup logs and the configure_environment MCP tool's reply.
func TestOpen_ErrorsNeverEchoTheCredential(t *testing.T) {
	cases := map[string]string{
		"mysql password, unreachable":    credDSN("mysql", "root:"+leakCanary, "127.0.0.1:1/app"),
		"mysql password, malformed url":  credDSN("mysql", "root:"+leakCanary, "[::1/app"),
		"postgres password, unreachable": credDSN("postgres", "thor:"+leakCanary, "127.0.0.1:1/thor?sslmode=disable"),
		"postgres password param":        "postgres://thor@127.0.0.1:1/thor?sslmode=disable&password=" + leakCanary,
		"postgres password, malformed":   credDSN("postgres", "thor:"+leakCanary, "[::1/thor"),
		"libsql token, unreachable":      "libsql://127.0.0.1:1?authToken=" + leakCanary,
		"libsql token, odd casing":       "libsql://127.0.0.1:1?AuthToken=" + leakCanary,
		"no scheme at all":               "root:" + leakCanary + "@tcp(db:3306)/app",
	}
	for name, dsn := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			conn, err := store.Open(ctx, dsn)
			if err == nil {
				_ = conn.Close()
				t.Skip("store opened; nothing to check (a server is listening on the canary port?)")
			}
			if strings.Contains(err.Error(), leakCanary) {
				t.Fatalf("open error echoes the credential: %v", err)
			}
		})
	}
}

// credDSN assembles "scheme://userinfo@rest" at run time. Fixture DSNs carry a
// fake credential on purpose; written as one literal they read, to a secret
// scanner, exactly like a committed real one (SEC-073).
func credDSN(scheme, userinfo, rest string) string {
	return scheme + "://" + userinfo + "@" + rest
}
