# API reference

The HTTP API is the source of truth — every SDK wraps the same endpoints. Full machine-readable spec: [`api/openapi.yaml`](https://github.com/klarlabs-studio/mnemos/blob/main/api/openapi.yaml).

## Endpoints (summary)

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/health` | Liveness probe |
| `GET` | `/v1/metrics` | Knowledge-base counts |
| `GET` `POST` | `/v1/events` | List / append events |
| `GET` `POST` | `/v1/claims` | List / upsert claims (with `as_of` + `recorded_as_of`) |
| `GET` `POST` | `/v1/relationships` | List / upsert claim-to-claim edges |
| `GET` `POST` | `/v1/embeddings` | List / upsert embeddings |
| `GET` `POST` | `/v1/search` | Hybrid retrieval over the claim store |
| `GET` `POST` | `/v1/context` | Render the Context Block for a run |

## Auth

JWT bearer on writes, open on reads. Mint via:

```bash
mnemos user create --name demo --email demo@example.com
mnemos token issue --user usr_...
```

Pass with `Authorization: Bearer <token>`.

## Context Block

`GET /v1/context?run_id=<id>` returns a stable, agent-ready string:

```
# Memory context (run <id>)
## Active claims (N)
- [cl_xxx · type · trust 0.91] text
## Contradictions (M)
- cl_xxx ⊥ cl_yyy
## Footer
Generated <ts>. claims=N contradictions=M
```

Designed to be dropped directly into an agent's system prompt. Layout is fixed so the agent can rely on it.

## MCP tools

`mnemos mcp` exposes the surface as MCP tools so Claude Code, Cursor, Cline, and other MCP-aware clients can call directly. Notable tools:

| Tool | Use |
|---|---|
| `query_knowledge` | search over claims |
| `process_text` | ingest raw text → events → claims → relationships |
| `record_decision` | persist an agent decision with belief claims + alternatives |
| `record_action` / `record_outcome` | log operational changes + their results |
| `synthesize_lessons` / `synthesize_playbooks` | derive higher-order patterns |
| `memory_deprecate` | mark a claim stale (letta-style self-edit) |
| `memory_resolve_contradiction` | pick winner of a contradicting pair |
| `memory_promote` | re-verify a claim against fresh evidence |
| `memory_context` | render the Context Block for a run |

The `memory_*` tools land letta-style agent-driven memory curation: the LLM decides what to deprecate / resolve / pin, Mnemos stores the audit trail.
