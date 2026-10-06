# Security

## Reporting a vulnerability

Email **felix.geelhaar@gmail.com** with `[MNEMOS SECURITY]` in the subject. Do not open a public issue. Expect an initial response within five business days.

## Threat model

Mnemos persists evidence-backed claims and serves them over CLI, MCP, HTTP REST, and gRPC. The trust boundary depends on the entrypoint:

- **CLI / MCP (stdio)**: trusted; the operator runs the binary locally and owns the database file.
- **HTTP registry (`mnemos serve`)**: writes (POST/PUT/DELETE) require a JWT bearer token issued by the same instance. Reads require a token too, by default. `serve --public-reads` (`MNEMOS_PUBLIC_READS`) opts in to anonymous GET reads for a browse-only dashboard on a trusted network; it is not for hosted deployments, and it never exposes `/internal/metrics`.
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

## Security gate and baseline

[`nox`](https://github.com/nox-hq/nox) scans every push. `scripts/nox-gate.sh` runs in warden's pre-push hook, the gate on the way to `main`. It runs a pinned nox release, checks the archive against a committed sha256 before running it, and fails on any **critical or high finding that `.nox/baseline.json` does not list**. The shared CI workflow pins the same nox version. That matters because fingerprints differ between nox releases.

The baseline says *what* is suppressed. **`.nox/waivers.yaml`** says *why*, *who* owns that decision, and *until when*. `test/security` fails when:

- a baseline entry has no waiver (same rule, file listed by name),
- a waiver has no owner or reason, or expires more than a year out,
- a waiver has expired: the risk must be re-decided, not inherited,
- a waiver no longer covers any entry.

Fix real findings. Waive only verified false positives and accepted gaps, with a reason that would convince a reviewer. Accepted hardening gaps carry shorter expiries.

Refresh after a fix, or when bumping nox:

```bash
NOX_GATE_WRITE_BASELINE=1 scripts/nox-gate.sh   # baseline = exactly the current findings
go test ./test/security/                         # then cover any new entry in .nox/waivers.yaml
```

## Known gaps

- mTLS between Mnemos and its consumers is operator-provided (TLS-terminating proxy or service mesh).
