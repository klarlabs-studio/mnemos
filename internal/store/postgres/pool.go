package postgres

import (
	"database/sql"
	"os"
	"strconv"
	"time"
)

// Default pool sizes. Postgres default max_connections is 100, so a
// pool cap of 25 leaves headroom for replicas and admin tooling. Five
// idle connections keep MCP burst latency low without pinning slots.
// 30-minute max lifetime sidesteps stale-connection failures behind
// load balancers that recycle TCP sessions.
const (
	defaultMaxOpenConns    = 25
	defaultMaxIdleConns    = 5
	defaultConnMaxLifetime = 30 * time.Minute
)

// tuneConnPool applies pool sizing to a freshly-opened *sql.DB.
// Operators can override each value via env (MNEMOS_DB_MAX_CONNS,
// MNEMOS_DB_MAX_IDLE_CONNS, MNEMOS_DB_CONN_MAX_LIFETIME) without
// touching the binary. Values are clamped to non-negative integers;
// negative inputs fall back to the default.
//
// Without these calls Go's database/sql defaults to unlimited open
// connections, which has bitten Mnemos under MCP burst load: a single
// `process_text` against postgres pool-thrashes and silently exhausts
// max_connections.
func tuneConnPool(db *sql.DB) {
	db.SetMaxOpenConns(envIntDefault("MNEMOS_DB_MAX_CONNS", defaultMaxOpenConns))
	db.SetMaxIdleConns(envIntDefault("MNEMOS_DB_MAX_IDLE_CONNS", defaultMaxIdleConns))
	db.SetConnMaxLifetime(envDurationDefault("MNEMOS_DB_CONN_MAX_LIFETIME", defaultConnMaxLifetime))
}

func envIntDefault(name string, fallback int) int {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

func envDurationDefault(name string, fallback time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return fallback
	}
	return d
}
