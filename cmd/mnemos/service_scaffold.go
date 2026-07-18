package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// service_scaffold.go implements the `mnemos init --service` bundle writer: it
// drops a ready-to-run hosted `mnemos serve` deployment into a directory
// (docker-compose + config + env template + README). The CLI dispatch that
// invokes scaffoldService is wired up separately; this file only owns the pure
// file-generation logic so it stays independently testable.
//
// The bundle defaults to Mode D (docs/deployment-modes.md): a hosted *product*
// deployment is multi-tenant (`serve --require-tenant`, Postgres row-level
// isolation), auth is mandatory on every request (reads and writes), and the
// JWT signing secret is required + persisted so issued tokens survive restarts.
// The README documents how to drop to single-tenant (Mode C) for a single-team
// backend.

// scaffoldFile pairs a bundle-relative filename with its rendered content.
type scaffoldFile struct {
	name    string
	content string
}

// serviceBundle returns the files that make up a hosted-deployment bundle, in
// deterministic order.
func serviceBundle() []scaffoldFile {
	return []scaffoldFile{
		{"docker-compose.yml", dockerComposeTemplate},
		{"mnemos.yaml", mnemosConfigTemplate},
		{".env.example", envExampleTemplate},
		{"README-mnemos-service.md", readmeTemplate},
	}
}

// scaffoldService writes a ready-to-run hosted-deployment bundle into outDir.
// force overwrites existing files; otherwise existing files are left untouched
// and reported as skipped. Returns the list of result lines (e.g. "wrote X",
// "skipped existing Y") for the caller to render.
func scaffoldService(outDir string, force bool) (written []string, err error) {
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return nil, fmt.Errorf("create output dir %s: %w", outDir, err)
	}

	results := make([]string, 0, len(serviceBundle()))
	for _, f := range serviceBundle() {
		path := filepath.Join(outDir, f.name)
		if !force && fileScaffoldExists(path) {
			results = append(results, fmt.Sprintf("skipped existing %s", path))
			continue
		}
		if err := os.WriteFile(path, []byte(f.content), 0o644); err != nil {
			return results, fmt.Errorf("write %s: %w", path, err)
		}
		results = append(results, fmt.Sprintf("wrote %s", path))
	}
	return results, nil
}

// fileScaffoldExists reports whether path refers to an existing file. Any stat
// error other than "not exist" is treated as present so we never silently
// clobber something we couldn't inspect.
func fileScaffoldExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// ---- bundle templates ----

const dockerComposeTemplate = `# mnemos hosted deployment — Postgres-backed ` + "`mnemos serve`" + `.
# Copy .env.example to .env and fill in the secrets before ` + "`docker compose up`" + `.
services:
  db:
    image: postgres:16
    environment:
      POSTGRES_USER: mnemos
      POSTGRES_DB: mnemos
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
    volumes:
      - mnemos-pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U mnemos"]
      interval: 5s
      timeout: 5s
      retries: 10
    restart: unless-stopped

  mnemos:
    image: ghcr.io/klarlabs/mnemos:latest
    # To build the image from this checkout instead of pulling it, comment out
    # the image: line above and uncomment the build below:
    # build: .
    # Mode D (hosted multi-tenant): every request must carry a tenant-scoped JWT
    # and data is physically isolated per tenant via Postgres row-level security.
    # For a single-team backend (Mode C) drop --require-tenant — see the README.
    command: serve --require-tenant
    depends_on:
      db:
        condition: service_healthy
    ports:
      - "8080:8080"
    environment:
      MNEMOS_DB_URL: postgres://mnemos:${POSTGRES_PASSWORD}@db:5432/mnemos
      # REQUIRED: a persisted signing secret so issued tokens survive restarts.
      # The stack cannot mint usable tokens until this is set (see .env.example).
      # ":?" fails the compose run early with a clear message if it is empty.
      MNEMOS_JWT_SECRET: ${MNEMOS_JWT_SECRET:?MNEMOS_JWT_SECRET must be set — generate one with: openssl rand -hex 32}
    healthcheck:
      test: ["CMD-SHELL", "wget -q -O- http://localhost:8080/health || exit 1"]
      interval: 10s
      timeout: 5s
      retries: 5
    restart: unless-stopped

volumes:
  mnemos-pgdata:
`

const mnemosConfigTemplate = `# mnemos.yaml — configuration for a hosted ` + "`mnemos serve`" + ` deployment.
#
# Environment variables (MNEMOS_*) override every value in this file, so in the
# compose setup the database URL and JWT secret come from the environment and
# the fields below are just documentation / local overrides.

db:
  # db.url is normally supplied via MNEMOS_DB_URL in docker-compose.yml
  # (postgres://mnemos:${POSTGRES_PASSWORD}@db:5432/mnemos), so leave it blank
  # here. Uncomment only to override the environment for a local run.
  # url: postgres://mnemos:change-me@localhost:5432/mnemos
  url: ""

serve:
  port: 8080
  # Mode D (default): multi-tenant with per-tenant physical isolation. Every
  # request must present a tenant-scoped JWT; --require-tenant is set on the
  # serve command in docker-compose.yml. Drop it there for a single-tenant
  # (Mode C) single-team backend.
  #
  # Enable TLS by pointing these at a cert/key pair mounted into the container.
  # Any real (networked) deployment should terminate TLS here or at a proxy.
  # tls_cert_file: /etc/mnemos/tls/server.crt
  # tls_key_file: /etc/mnemos/tls/server.key
`

const envExampleTemplate = `# copy to .env and fill in, then run: docker compose up -d
#
# Postgres password used by both the db service and the MNEMOS_DB_URL it builds.
POSTGRES_PASSWORD=change-me

# REQUIRED — JWT signing secret for issued API tokens.
#
# This MUST be set to a strong, persisted value before the stack can mint usable
# tokens. Leaving it empty is not "off": mnemos would auto-generate a fresh
# secret on every restart, silently invalidating every token already issued.
# Set it once here and keep it stable for the life of the deployment.
#
# Generate a strong value with:
#   openssl rand -hex 32
#
# docker-compose.yml refuses to start (via ` + "`${MNEMOS_JWT_SECRET:?...}`" + `) until
# this is non-empty.
MNEMOS_JWT_SECRET=
`

const readmeTemplate = `# mnemos service

A hosted ` + "`mnemos serve`" + ` deployment: Postgres for storage and the mnemos
HTTP/gRPC API in front of it, wired together with docker-compose.

This bundle defaults to **Mode D — hosted multi-tenant** (see the project's
` + "`docs/deployment-modes.md`" + `): the service runs ` + "`serve --require-tenant`" + `, so
data is physically isolated per tenant (Postgres row-level security) and **every
request — reads and writes alike — must present a tenant-scoped JWT**. This is
the correct shape for a product serving many customers. For a single team's
backend, see "Single-tenant" below.

## Preflight — the signing secret is required

The stack **cannot issue usable tokens** until ` + "`MNEMOS_JWT_SECRET`" + ` is set to a
strong, persisted value. Leaving it empty is not "auth off": mnemos would
auto-generate a new secret on every restart and silently invalidate every token
already minted. Set it once and keep it stable. ` + "`docker-compose.yml`" + ` refuses
to start until it is non-empty.

## Run it

1. Create your environment file and fill in the secrets:

   ` + "```sh" + `
   cp .env.example .env
   # edit .env:
   #   - set POSTGRES_PASSWORD
   #   - set MNEMOS_JWT_SECRET (REQUIRED, persisted): openssl rand -hex 32
   ` + "```" + `

2. Start the stack:

   ` + "```sh" + `
   docker compose up -d
   ` + "```" + `

3. Wait for the mnemos service to report healthy:

   ` + "```sh" + `
   docker compose ps            # STATUS should show "healthy"
   curl -sf http://localhost:8080/health
   ` + "```" + `

## Consuming the API

- REST endpoints live under ` + "`http://localhost:8080/v1/...`" + `
- Health check: ` + "`http://localhost:8080/health`" + `
- Prometheus metrics: ` + "`http://localhost:8080/internal/metrics`" + `
- A gRPC endpoint is also served (enable/configure it via ` + "`mnemos serve --grpc-port`" + `);
  see the project docs for the service definitions.

## Observability (Prometheus + Grafana)

` + "`mnemos serve`" + ` emits product + cognitive metrics (ADR 0020) and structured JSON
logs (ADR 0021). A ready-to-import bundle — a Grafana dashboard, Prometheus
alert rules with thresholds matched to the brain-health verdict, and a scrape
config — ships in the repo at ` + "`deploy/observability/`" + ` (ADR 0022). Point your
existing Prometheus/Grafana at this deployment:

- **Scrape** ` + "`/internal/metrics`" + `. It is **authenticated by default** and is not
  covered by ` + "`--public-reads`" + `, so either give Prometheus a bearer token
  (` + "`mnemos token issue --user prometheus --tenant <id>`" + ` → a ` + "`0600`" + ` file
  referenced via ` + "`authorization: credentials_file:`" + `) or run
  ` + "`serve --metrics-public`" + ` on a trusted internal network and scrape anonymously.
- **Import** ` + "`deploy/observability/grafana-dashboard.json`" + ` into Grafana and load
  ` + "`deploy/observability/alerts.yml`" + ` into Prometheus. See that folder's README
  for the step-by-step, including shipping the logs to Loki.

## Authentication (multi-tenant)

Every request — reads and writes — must carry a JWT signed with
` + "`MNEMOS_JWT_SECRET`" + `. Under ` + "`--require-tenant`" + ` the token must be
**tenant-scoped**: mint one with an explicit ` + "`--tenant`" + `:

` + "```sh" + `
mnemos token issue --user <user-id> --tenant <tenant-id>
` + "```" + `

(Run ` + "`mnemos token --help`" + ` for TTL, agent tokens, and revocation flags.)

Send it as a bearer token on every call:

` + "```sh" + `
curl -H "Authorization: Bearer <token>" http://localhost:8080/v1/...
` + "```" + `

The effective tenant is taken **from the token** (its ` + "`tnt`" + ` claim); the
request is physically scoped to that tenant's data. A token granted more than
one tenant (a ` + "`tnts`" + ` allowlist) may select one per request with the
` + "`X-Mnemos-Tenant`" + ` header, but only within its grant. There is no anonymous
access: reads are authenticated just like writes.

## Transport security (TLS)

Any real (networked) deployment should encrypt in transit. Terminate TLS at a
reverse proxy in front of mnemos, or point mnemos at a cert/key pair by
uncommenting ` + "`tls_cert_file`" + ` / ` + "`tls_key_file`" + ` in ` + "`mnemos.yaml`" + ` (or set
` + "`MNEMOS_TLS_CERT_FILE`" + ` / ` + "`MNEMOS_TLS_KEY_FILE`" + `) and mounting the files into
the container. mTLS is available via ` + "`MNEMOS_MTLS_CLIENT_CA_FILE`" + `.

## Single-tenant (Mode C)

For a single team or app backend — one shared dataset behind auth, no per-tenant
isolation — drop ` + "`--require-tenant`" + ` from the ` + "`command:`" + ` in
` + "`docker-compose.yml`" + ` so it reads ` + "`command: serve`" + `. Auth is still mandatory
on every request; tokens no longer need a ` + "`--tenant`" + `:

` + "```sh" + `
mnemos token issue --user <user-id>
` + "```" + `

The persisted ` + "`MNEMOS_JWT_SECRET`" + ` is required in this mode too.

## Configuration

` + "`mnemos.yaml`" + ` documents the tunable config keys. Environment variables
(` + "`MNEMOS_*`" + `) override the file, which is how the database URL and JWT secret are
injected in ` + "`docker-compose.yml`" + `.
`
