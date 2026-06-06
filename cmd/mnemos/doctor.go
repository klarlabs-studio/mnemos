package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/store"
)

// handleDoctor implements `mnemos doctor [--json]`. Surfaces every
// subsystem the reliability work cares about — DB, providers, JWT
// secret, project root, axi-go kernel — as a single PASS/FAIL/SKIP
// table the operator can scan on a fresh install or after an outage.
//
// Exits with code 0 when nothing failed (skipped checks are fine);
// exits with code 1 when any check is "failed", so CI can use it as
// a smoke test.
func handleDoctor(args []string, _ Flags) {
	jsonOut := false
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		default:
			exitWithMnemosError(false, NewUserError("unknown doctor flag %q", a))
			return
		}
	}

	report := runDoctorChecks(context.Background())
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
	} else {
		printDoctorHuman(report)
	}
	if !report.Healthy {
		os.Exit(int(ExitError))
	}
}

// runDoctorChecks runs every probe doctor cares about. Each result
// goes into a single ordered list so the human output reads
// top-to-bottom in startup order.
func runDoctorChecks(ctx context.Context) healthCheckResult {
	checks := []healthCheck{
		probeProjectRoot(),
		probeJWTSecret(),
	}

	dbCheck, conn := probeDoctorDB(ctx)
	checks = append(checks, dbCheck)
	if conn != nil {
		defer func() { _ = conn.Close() }()
		// The deep DB write probe is currently SQLite-specific (it
		// inserts into revoked_tokens inside a rolled-back tx). Other
		// backends won't expose a *sql.DB through Raw, in which case
		// we skip the deep write probe rather than fail it — the
		// open check above already proved the backend is reachable.
		if rawDB, ok := conn.Raw.(*sql.DB); ok && rawDB != nil {
			checks = append(checks, probeDB(ctx, rawDB))
		} else {
			checks = append(checks, healthCheck{
				Name:   "sqlite",
				Status: "skipped",
				Detail: "non-sqlite backend: write probe not implemented",
			})
		}
	}

	checks = append(checks,
		probeLLM(ctx),
		probeEmbedding(ctx),
	)

	healthy := true
	for _, c := range checks {
		if c.Status == "failed" {
			healthy = false
		}
	}
	return healthCheckResult{Healthy: healthy, Checks: checks}
}

func probeProjectRoot() healthCheck {
	dbPath, projectRoot, ok := findProjectDB()
	if !ok {
		return healthCheck{Name: "project_root", Status: "skipped", Detail: "no .mnemos/ found walking up from CWD (using XDG default)"}
	}
	return healthCheck{Name: "project_root", Status: "ok", Detail: fmt.Sprintf("root=%s db=%s", projectRoot, dbPath)}
}

// probeJWTSecret reports the state of JWT signing material without
// implicitly creating it. Doctor is a diagnostic, not a setup step,
// so we should not silently materialise a secret on a fresh install.
//
// States:
//   - "ok"      MNEMOS_JWT_SECRET is set and valid, OR a secret file
//     exists at the resolved path and loads cleanly.
//   - "failed"  Configured (env or existing file) but malformed.
//   - "skipped" Auth has not been configured. Reports the path the
//     binary would write to, so users in read-only-rootfs
//     containers (#21) see how to point MNEMOS_AUTH_DIR or
//     MNEMOS_JWT_SECRET at a writable location before they
//     start `mnemos serve` or `token issue`.
func probeJWTSecret() healthCheck {
	if envHex := strings.TrimSpace(os.Getenv("MNEMOS_JWT_SECRET")); envHex != "" {
		if _, _, err := auth.LoadOrCreateSecret(""); err != nil {
			return healthCheck{Name: "jwt_secret", Status: "failed", Detail: err.Error()}
		}
		return healthCheck{Name: "jwt_secret", Status: "ok", Detail: "from MNEMOS_JWT_SECRET"}
	}

	_, projectRoot, _ := findProjectDB()
	path := auth.DefaultSecretPath(projectRoot)
	if path == "" {
		return healthCheck{Name: "jwt_secret", Status: "skipped", Detail: "no $HOME, no project root, no MNEMOS_AUTH_DIR — set MNEMOS_JWT_SECRET to use auth"}
	}
	if _, err := os.Stat(path); err == nil {
		if _, _, err := auth.LoadOrCreateSecret(path); err != nil {
			return healthCheck{Name: "jwt_secret", Status: "failed", Detail: err.Error()}
		}
		return healthCheck{Name: "jwt_secret", Status: "ok", Detail: "loadable from " + path}
	}
	return healthCheck{
		Name:   "jwt_secret",
		Status: "skipped",
		Detail: fmt.Sprintf("not configured; will be created at %s on first use of `serve` or `token issue` (set MNEMOS_AUTH_DIR or MNEMOS_JWT_SECRET to override)", filepath.Dir(path)),
	}
}

// probeDoctorDB opens the configured backend through the store
// registry and returns both the result and the open Conn so the
// deeper write probe can reuse it. We treat "can't open" and
// "can't write" as separate checks so the human output reads
// "store_open failed" rather than an opaque write-probe failure
// when the file is missing or the DSN is malformed.
//
// The check name stays "store_open" (rather than "sqlite_open") so
// the doctor output is honest about what it actually probed once
// non-SQLite backends are wired in.
func probeDoctorDB(ctx context.Context) (healthCheck, *store.Conn) {
	dsn := resolveDSN()
	conn, err := openConn(ctx)
	if err != nil {
		return healthCheck{Name: "store_open", Status: "failed", Detail: fmt.Sprintf("dsn=%s: %s", dsn, err.Error())}, nil
	}
	return healthCheck{Name: "store_open", Status: "ok", Detail: dsn}, conn
}

func printDoctorHuman(r healthCheckResult) {
	fmt.Printf("mnemos doctor — %s\n\n", strings.ToUpper(ternary(r.Healthy, "ok", "degraded")))
	for _, c := range r.Checks {
		mark := "✓"
		switch c.Status {
		case "failed":
			mark = "✗"
		case "skipped":
			mark = "·"
		}
		fmt.Printf("  %s %-16s %-8s %s\n", mark, c.Name, c.Status, c.Detail)
	}
	fmt.Println()
	if !r.Healthy {
		fmt.Println("at least one check failed; mnemos may not be fully operational")
	} else {
		fmt.Println("all required subsystems healthy")
	}
}
