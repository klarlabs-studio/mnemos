# TypeScript SDK (`mnemos-ts`)

Optional thin wrapper. Bring your own `fetch` (defaults to global) so it runs in Node 18+, browsers, Deno, Bun, and edge runtimes.

```bash
npm install mnemos-ts
```

```ts
import { Mnemos } from "mnemos-ts";

const m = new Mnemos({ baseUrl: "http://localhost:7777" });

const run = m.startRun("chat-session-1");
await run.remember("user prefers vegetarian options", { role: "preference" });
await run.remember("user is allergic to peanuts", { role: "preference" });

for (const memory of await run.recall()) {
  console.log(memory.timestamp, memory.content);
}

// Hybrid retrieval
const hits = await m.search("dietary restrictions", { topK: 5, minTrust: 0.5 });

// Context Block
const block = await m.context(run.runId);
```

## API surface

| Method | Wraps |
|---|---|
| `m.startRun(subject)` | local helper — mints a UUID |
| `run.remember(content, opts)` | `POST /v1/events` |
| `run.recall(limit?)` | `GET /v1/events?run_id=…` |
| `m.appendEvent(runId, content, metadata?, sourceInputId?)` | `POST /v1/events` |
| `m.listEvents(runId?, limit?)` | `GET /v1/events` |
| `m.search(query, opts)` | `GET /v1/search` |
| `m.context(runId, opts)` | `GET /v1/context` |
| `m.health()` | `GET /health` |

Source + tests: [github.com/klarlabs-studio/mnemos-ts](https://github.com/klarlabs-studio/mnemos-ts).
