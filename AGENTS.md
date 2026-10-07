# AGENTS

## Repo reality

Mnemos is the **local-first evidence layer** of the cognitive stack (Mnemos → Chronos → Nous → Praxis). Code is feature-complete through Phase 8: claim extraction, contradiction detection, causal edges, action+outcome recording, lesson + playbook synthesis, decision audit, multi-tenant scope, markdown round-trip, multi-backend storage, HTTP REST, gRPC, MCP. See `README.md` for the user-facing surface and `CLAUDE.md` for architecture and conventions.

## Verified sources of truth

- **Product / strategy**: `PRD.md`, `Product Brief.md`, `Vision.md`
- **Technical design**: `TDD.md` (covers MVP domain — Phase 2+ schema lives in `sql/sqlite/schema.sql` + ADRs)
- **Architecture decisions**: `docs/adr/` (currently `0001-multi-backend-storage.md`)
- **Roadmap**: `Roadmap.md` — phase status: 1, 2A, 2B, A, F, axi-go all SHIPPED; Phase 3 (v1.0) FUTURE
- **Execution state**: `.roady/` workspace (spec, plan, state)
- **Architecture overview for AI agents**: `CLAUDE.md`

## Build, test, lint

```bash
make check          # fmt + lint + test + build (CI equivalent)
make build          # bin/mnemos
make test           # go test -race -count=1 ./... (102 eval cases)
make sqlc           # regen sqlc from sql/sqlite/queries
make release-check  # validate .goreleaser.yaml
```

CI: `.github/workflows/ci.yml` runs format → vet → golangci-lint v2.1 → race tests → build → goreleaser check on push/PR to main.

## Structure and boundaries

- `cmd/mnemos/` — CLI subcommands; one file per command; `mcp` and `serve` (HTTP + gRPC) live alongside
- `internal/` — packages by domain (extract, relate, query, synthesize, store/{sqlite,memory,postgres,mysql,libsql}, ...)
- `proto/mnemos/v1/mnemos.proto` — gRPC schema; `proto/gen/` holds generated code
- `sql/sqlite/` — schema + sqlc query source; regenerate after edits via `make sqlc`
- `docs/` — guides (`library.md`, `integrations.md`, …), `adr/` decisions, `reference/` generated references, `notes/` dated working notes; `docs/README.md` indexes them
- `client/` — typed Go client for the HTTP registry
- `data/` — local ingestion artifacts (gitignored except `.gitkeep`)
- `.relicta/` — release tooling metadata; not product source

## Conventions

- Conventional Commits.
- Stdlib + project-owned libraries (`bolt` logging, `fortify` retry, `statekit` state machine, `mcp-go`).
- `CGO_ENABLED=0` — pure-Go SQLite via `modernc.org/sqlite`.
- All repository methods take `context.Context` first.
- Domain types ship `Validate()`; contradictions are first-class.
- Backends register from `init()` against the URL-scheme dispatcher in `internal/store`.

## Open items (see Roadmap.md for full list)

- gRPC API has no dedicated README section beyond the registry block — covered in `proto/mnemos/v1/mnemos.proto`.
- `TDD.md` covers MVP only; Phase 2+ schema (actions, outcomes, lessons, decisions, playbooks, scopes, causal edges) lives in `sql/sqlite/schema.sql`.
