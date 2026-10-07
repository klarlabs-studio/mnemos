# LinkedIn launch draft — Mnemos

## Post (1300 char ceiling, sweet spot ~700)

Just shipped Mnemos — an open-source memory layer for AI apps you can self-host.

The market has hosted memory services (great if you're fine sending your customer data to someone's cloud). It has vector DBs (great for similarity search, not for claim-level reasoning). It has notes apps (great for humans, not for AI agents at scale).

Mnemos is the option for stacks that can't leave your servers — regulated industry, on-prem, air-gapped, or "we don't trust someone else's cloud with our agent's memory."

What makes it different beyond self-hosting:

→ Contradiction detection. Two facts disagree, Mnemos surfaces it before your agent quotes either.

→ Evidence-back-to-source. Every claim links to the event it came from. No more "trust the model" hallucination bisection.

→ Replay-by-run-id. Group events by chat session or agent run, replay months later from one HTTP call.

There's no SDK to install. The whole simple-case memory API is 5 lines of raw HTTP — your code, your dependencies, no opinionated wrapper. A 30-line quickstart chatbot and a 4-node LangGraph refund-triage example ship in the repo.

Stack: Go (single binary), MIT licensed. SQLite by default; Postgres, MySQL, libSQL, in-memory all supported.

Live: https://klarlabs-studio.github.io/mnemos
Repo: https://github.com/klarlabs-studio/mnemos

Looking for design partners running AI agents in regulated environments — DM if that's you.

#AI #OpenSource #Memory #SelfHosted

## Posting checklist

- [ ] Pages deploy green
- [ ] Same week as HN, different day
- [ ] Engage every comment in first 90 minutes
- [ ] Post Tuesday-Thursday, 7-9am or 12-1pm local time
- [ ] Don't post identical text on Olymp + Mnemos within 3 weeks of each other
- [ ] Don't @ named competitors (no flame-war shape)
