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
    command: serve
    depends_on:
      db:
        condition: service_healthy
    ports:
      - "8080:8080"
    environment:
      MNEMOS_DB_URL: postgres://mnemos:${POSTGRES_PASSWORD}@db:5432/mnemos
      MNEMOS_JWT_SECRET: ${MNEMOS_JWT_SECRET}
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
  # Enable TLS by pointing these at a cert/key pair mounted into the container.
  # tls_cert_file: /etc/mnemos/tls/server.crt
  # tls_key_file: /etc/mnemos/tls/server.key
`

const envExampleTemplate = `# copy to .env and fill in, then run: docker compose up -d
#
# Postgres password used by both the db service and the MNEMOS_DB_URL it builds.
POSTGRES_PASSWORD=change-me

# JWT signing secret for issued API tokens. Generate a strong value with:
#   openssl rand -hex 32
MNEMOS_JWT_SECRET=
`

const readmeTemplate = `# mnemos service

A hosted ` + "`mnemos serve`" + ` deployment: Postgres for storage and the mnemos
HTTP/gRPC API in front of it, wired together with docker-compose.

## Run it

1. Create your environment file and fill in the secrets:

   ` + "```sh" + `
   cp .env.example .env
   # edit .env: set POSTGRES_PASSWORD and generate MNEMOS_JWT_SECRET
   #   openssl rand -hex 32
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

## Authentication

The API expects a JWT signed with ` + "`MNEMOS_JWT_SECRET`" + `. Mint one with the
` + "`mnemos token`" + ` command (run ` + "`mnemos token --help`" + ` for the exact flags), then
send it as a bearer token:

` + "```sh" + `
curl -H "Authorization: Bearer <token>" http://localhost:8080/v1/...
` + "```" + `

## Configuration

` + "`mnemos.yaml`" + ` documents the tunable config keys. Environment variables
(` + "`MNEMOS_*`" + `) override the file, which is how the database URL and JWT secret are
injected in ` + "`docker-compose.yml`" + `.
`
