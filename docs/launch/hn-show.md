# Show HN draft — Mnemos

## Title (80 char limit, ~70 sweet spot)

Pick one. A is the sharpest claim, B is the most concrete, C is the most contrarian.

**A.** Show HN: Mnemos – self-hosted memory for AI apps, 5 lines of HTTP, no SDK

**B.** Show HN: Mnemos – AI memory layer with contradiction detection and replay

**C.** Show HN: Why we don't ship a Python SDK for our AI memory layer

## URL

https://klarlabs-studio.github.io/mnemos/

## Body (HN's optional comment field — first comment of the thread)

Hey HN,

Mnemos is an open-source memory layer for AI apps. Designed for the case where your customer data can't leave your servers — regulated industry, on-prem, air-gapped, or just "we don't trust someone else's cloud with our agent's memory."

There's no SDK to install. The whole simple-case API is two HTTP calls (POST /v1/events to remember, GET /v1/events?run_id=… to recall). I wrote it that way deliberately: every language already has an HTTP client, and "pip install mnemos" would lock you into my opinionated wrapper for something that's already five lines.

What I think makes it interesting beyond "another self-hosted memory store":

1. **Contradiction detection.** Two facts disagree, Mnemos surfaces it via a relationships table (supports / contradicts / causes / etc.) rather than letting your agent quote both confidently.

2. **Evidence-back-to-source.** Every claim links to the event(s) it was extracted from. Hallucination bisection becomes "follow the link," not "re-prompt and pray."

3. **Replay-by-run-id.** Group events by run, replay the whole chain months later from one HTTP call. Makes audit + debugging tractable for production agents.

4. **Local-first.** SQLite default (pure-Go, no CGO). Postgres / MySQL / libSQL / in-memory all supported via a registered-scheme dispatcher.

There's a 30-line quickstart chatbot example showing the basic memory loop, and a 4-node LangGraph refund-triage example showing the production audit shape with run-id replay.

Stack: Go (single binary), MIT licensed, GHCR image, gRPC alongside HTTP, MCP server for Claude Code / Cursor / Cline.

Repo: https://github.com/klarlabs-studio/mnemos

Honest about scope: Mnemos doesn't host anything for you. It's a binary you run. If "easiest possible onboarding to managed memory" is what you need, hosted services do that better than I can. If you want substrate you own, this is for you.

Happy to take questions.

## Posting checklist

- [ ] Pages deploy green
- [ ] 5-line snippet in hero copy-pasteable
- [ ] Quickstart chatbot README runs end-to-end
- [ ] Refund-triage example linked
- [ ] Be online for 4 hours after posting
- [ ] Don't post on Friday afternoon, Sunday, or holidays
- [ ] Best time: Tuesday-Thursday, 8-10am PT
- [ ] Don't post Mnemos and Olymp on the same day — split by 2-3 weeks
