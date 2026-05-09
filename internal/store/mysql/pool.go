package mysql

import (
	"database/sql"
	"os"
	"strconv"
	"time"
)

// Default pool sizes mirror the Postgres provider so operators get the
// same dials regardless of backend. MySQL's max_connections default is
// 151; capping at 25 leaves room for replicas, admin clients, and the
// occasional probe.
const (
	defaultMaxOpenConns    = 25
	defaultMaxIdleConns    = 5
	defaultConnMaxLifetime = 30 * time.Minute
)

// tuneConnPool applies pool sizing to a freshly-opened *sql.DB.
// Env overrides: MNEMOS_DB_MAX_CONNS, MNEMOS_DB_MAX_IDLE_CONNS,
// MNEMOS_DB_CONN_MAX_LIFETIME (e.g. "30m"). Negative or unparseable
// values fall back to the default.
//
// Without these calls database/sql defaults to unlimited open
// connections — under MCP burst load this exhausts MySQL
// max_connections and the process leaks idle TCP sockets through
// load balancers that recycle them.
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
