# Security

## Reporting a vulnerability

Email **felix.geelhaar@gmail.com** with `[MNEMOS SECURITY]` in the subject. Do not open a public issue. Expect an initial response within five business days.

## Threat model

Mnemos persists evidence-backed claims and serves them over CLI, MCP, HTTP REST, and gRPC. The trust boundary depends on the entrypoint:

- **CLI / MCP (stdio)**: trusted; the operator runs the binary locally and owns the database file.
- **HTTP registry (`mnemos serve`)**: writes (POST/PUT/DELETE) require a JWT bearer token issued by the same instance. Reads are open by default — appropriate for browse-only dashboards on a trusted network.
- **gRPC server (`mnemos serve --grpc-port`)**: every RPC is gated by the same JWT verifier. Configure via `MNEMOS_JWT_SECRET` (hex-encoded ≥ 32 bytes) or the per-install secret file under `MNEMOS_AUTH_DIR`. Issue tokens with `mnemos token issue`; revoke with `mnemos token revoke`.

Production deployments must:

- Run behind TLS at the ingress.
- Configure `MNEMOS_JWT_SECRET` (or a writable `MNEMOS_AUTH_DIR`) and rotate signing keys periodically.
- Issue scoped tokens via `mnemos token issue --scopes <list> --runs <list>` to limit what each client can do.
- Treat the database file (`~/.local/share/mnemos/mnemos.db` by default, or whatever `MNEMOS_DB_URL` points at) as PII-bearing — back up, encrypt at rest if the underlying engine supports it, and restrict filesystem access.

## Authentication surfaces

| Surface | Mechanism | Env var | Default behaviour |
|---|---|---|---|
| HTTP reads | none | – | open |
| HTTP writes | JWT (HS256) | `MNEMOS_JWT_SECRET` or `MNEMOS_AUTH_DIR/jwt-secret` | disabled if no verifier configured (local dev only) |
| gRPC (all RPCs) | JWT (HS256) — same verifier as HTTP | `MNEMOS_JWT_SECRET` or `MNEMOS_AUTH_DIR/jwt-secret` | disabled if no verifier configured (local dev only) |
| MCP | none (stdio is in-process) | – | – |
| Registry push/pull (client side) | bearer token sent to remote | `MNEMOS_REGISTRY_TOKEN` (CLI flag `--token` overrides) | required when remote registry sets one |

Token issuance and revocation: `mnemos token issue|revoke|list`. Revocations are checked via the `RevokedTokens` repository on every gRPC RPC.

## Container

The Docker image is `alpine:3.21` based and runs as the unprivileged `mnemos` user. Run with `--read-only` and a writable volume if the rootfs is read-only:

```bash
docker run --read-only \
  -v mnemos-data:/home/mnemos/.local/share/mnemos \
  -v mnemos-auth:/home/mnemos/.mnemos \
  -e MNEMOS_AUTH_DIR=/home/mnemos/.mnemos \
  -e MNEMOS_JWT_SECRET=<hex-32-bytes> \
  # MNEMOS_REGISTRY_TOKEN is for client-side push/pull; not needed for inbound auth.
  -p 7777:7777 \
  ghcr.io/klarlabs-studio/mnemos serve --grpc-port 7778
```

Pin the base image to a digest before deploying to production:

```bash
docker buildx imagetools inspect alpine:3.21
# update Dockerfile FROM line with the returned sha256:... digest
```

## Dependencies

Direct dependencies tracked in `go.mod`. Refresh:

```bash
go get -u ./...
go mod tidy
make check       # fmt + lint + test + build
```

## Data sensitivity

Mnemos stores claims, evidence events, embeddings, and synthesised lessons. Operators should:

- **Not** ingest secrets, credentials, or personally identifying data unless the deployment treats the database as a sensitive store.
- Use `mnemos delete-event <id>...` and `mnemos delete-claim <id>...` to remove material that should not have been ingested. Cascades to derived state.
- Use `mnemos audit` to export the full knowledge base for compliance review.

## Secrets

No secrets are stored in source. JWT signing material lives in `MNEMOS_AUTH_DIR/jwt-secret` (auto-created with 0600 permissions on first run) or in `MNEMOS_JWT_SECRET`. LLM API keys come from `MNEMOS_LLM_API_KEY` / `MNEMOS_EMBED_API_KEY` at process start.

## Security baseline (`findings.json`)

`findings.json` is the committed baseline of [`nox`](https://github.com/felixgeelhaar/nox) v0.7.0 scan results. Every finding has `Status: "baselined"` — meaning it was reviewed and accepted as known-and-acceptable. New scans diff against this file in CI; any finding **not** present in the baseline fails the build.

Categories tracked:

- **`IAC-254` / `IAC-351` (critical)** — CI workflow creds. False positives: postgres/mysql container creds for ephemeral test databases in `.github/workflows/ci.yml`. Not production secrets.
- **`AI-006` (medium)** — prompt/LLM responses logged. Reviewed: log statements emit metadata only, not raw prompts.
- **`DATA-001` (low)** — test-data emails / IDs in test fixtures.

Refresh:

```bash
make nox-scan          # produces findings.json
# review the diff; mark any new genuine concerns as "baselined" only
# after triage. Unexplained additions block merge.
git add findings.json
```

## Known gaps

- mTLS between Mnemos and its consumers is operator-provided (TLS-terminating proxy or service mesh).
