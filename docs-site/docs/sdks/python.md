# Python SDK (`mnemos-py`)

Optional thin wrapper around the HTTP API. Use it for typed return values and pip-install ergonomics; raw `httpx` against the server is fully supported alternative.

```bash
pip install mnemos-py
```

```python
from mnemos import Mnemos

with Mnemos(base_url="http://localhost:7777") as m:
    run = m.start_run(subject="chat-session-1")
    run.remember("user prefers vegetarian options", role="preference")
    run.remember("user is allergic to peanuts", role="preference")

    for memory in run.recall():
        print(memory.timestamp, memory.content)

    # Hybrid retrieval
    hits = m.search("dietary restrictions", top_k=5, min_trust=0.5)

    # Context Block — drop into a system prompt
    block = m.context(run_id=run.run_id)
```

## API surface

| Method | Wraps |
|---|---|
| `Mnemos.start_run(subject)` | local helper — mints a UUID |
| `run.remember(content, role, metadata)` | `POST /v1/events` |
| `run.recall(limit=200)` | `GET /v1/events?run_id=…` |
| `m.append_event(run_id, content, metadata)` | `POST /v1/events` |
| `m.list_events(run_id?, limit=200)` | `GET /v1/events` |
| `m.search(query, run_id?, top_k, min_trust?, as_of?)` | `GET /v1/search` |
| `m.context(run_id, query?, max_tokens?)` | `GET /v1/context` |
| `m.health()` | `GET /health` |

Source + tests: [github.com/klarlabs-studio/mnemos-py](https://github.com/klarlabs-studio/mnemos-py).

## Auth

Set `MNEMOS_JWT` env or pass `token=` to the constructor. Reads are open; mutating methods require a valid token signed against the server's secret.
