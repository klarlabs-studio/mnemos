# Mnemos documentation

Four kinds of document live here, kept apart so each is trusted for what it is.

- **Decisions** (`adr/`) record why something is the way it is, at the time it was decided. They are not rewritten afterwards; a later ADR supersedes an earlier one.
- **Generated reference** (`reference/`) is produced from the code and checked against it by a test. Edit the source, not the file.
- **Guides** (this directory) are curated and maintained: how to use, deploy and operate Mnemos.
- **Working notes** (`notes/`) are dated plans, reviews, research and drafts. They record thinking at a point in time and are not kept current.

`TestDocsIndex` fails when a guide, ADR or note is missing from this page, or when a file in `reference/` is not generated.

## Generated reference

- [Capabilities by transport](reference/capabilities.md) (`go run ./tools/capdoc`)
- [Configuration](reference/configuration.md) (`go run ./tools/configdoc`)
- [REST API (OpenAPI)](../api/openapi.yaml)

## Guides

- [Mnemos as a Go library](library.md)
- [Mnemos Integration Guide](integrations.md)
- [Mnemos deployment & access modes](deployment-modes.md)
- [Telemetry](telemetry.md)
- [Vocabulary: where each term is used](vocabulary.md)
- [Central + repo-isolated brain workflow](repo-brain-workflow.md)
- [Mutation testing](testing/mutation.md)
- [Consolidation baseline (Phase 0, #382)](consolidation/baseline/README.md)

## Decisions

- [ADR 0001: Multi-Backend Storage with Pluggable Providers](adr/0001-multi-backend-storage.md)
- [ADR 0002: Cross-entity edges and pull-based outcome adapters](adr/0002-cross-entity-edges-and-outcome-pull-adapters.md)
- [ADR 0003: Archive Olymp](adr/0003-archive-olymp.md)
- [ADR 0004: Extract decisionkit from Nous](adr/0004-extract-decisionkit.md)
- [ADR 0005: Archive Nous](adr/0005-archive-nous.md)
- [ADR 0006: Archive Praxis](adr/0006-archive-praxis.md)
- [ADR 0007: Per-Tenant Scoping Within a Namespace](adr/0007-per-tenant-scoping.md)
- [ADR 0008: `$HOME/.mnemos` is the global fallback, not a project root](adr/0008-home-mnemos-not-a-project-root.md)
- [ADR 0009: Repo-as-tenant in a hosted central brain, with federated reads](adr/0009-hosted-repo-tenant-federation.md)
- [ADR 0010: User-selected workspaces (the Cowork model)](adr/0010-user-selected-workspaces.md)
- [ADR 0011: Mnemos as a brain — Complementary Learning Systems, consolidation, and the ubiquitous language](adr/0011-brain-consolidation-cls.md)
- [ADR 0012: Subject-classified promotion — individual stays private, class can go global](adr/0012-knowledge-topology-subject-classified-promotion.md)
- [ADR 0013: Cognitive completeness — the brain/NN mechanisms Mnemos still lacks](adr/0013-cognitive-completeness-roadmap.md)
- [ADR 0014: Credit assignment — outcomes update belief trust](adr/0014-credit-assignment.md)
- [ADR 0015: Learning dynamics — replay, association plasticity, and neuromodulation](adr/0015-learning-dynamics.md)
- [ADR 0016: Competitive inhibition — retrieval-induced forgetting](adr/0016-competitive-inhibition.md)
- [ADR 0017: Predictive coding — the hierarchical prediction-error surface](adr/0017-predictive-coding.md)
- [ADR 0018: The cognitive journal — instrumentation for studying learning](adr/0018-cognitive-journal.md)
- [ADR 0019: Brain health — vital signs, integrity checks, and health-over-time](adr/0019-brain-health.md)
- [ADR 0020: Operational metrics — product + cognitive signals for Prometheus/Grafana](adr/0020-operational-metrics.md)
- [ADR 0021: Observability logging — structured logs for the important brain operations](adr/0021-observability-logging.md)
- [ADR 0022: Observability bundle — shipped Grafana dashboard, alert rules, and scrape config](adr/0022-observability-bundle.md)
- [ADR 0023: Observations are not beliefs — route operational events to the episodic layer](adr/0023-observations-vs-knowledge.md)
- [ADR 0024: Graded retrievability — separating storage strength from retrieval strength](adr/0024-graded-retrievability.md)
- [ADR 0025: `half_life_days = 0` is two different facts — record classification provenance separately](adr/0025-half-life-classification-provenance.md)
- [ADR 0026: One canonical trust value per belief, per instant](adr/0026-canonical-trust.md)
- [ADR 0027: A per-claim budget on inferred supports edges](adr/0027-supports-edge-budget.md)
- [ADR 0028: A computation budget on relate's candidates](adr/0028-relate-candidate-budget.md)
- [ADR 0029: Derived state records what produced it](adr/0029-derived-state-records-its-producer.md)

## Working notes

- [backlog.md](notes/backlog.md)
- [Phase 2: Make the Engine Trustworthy](notes/phase2-plan.md)
- [Mnemos CLI — UX review of commands & flags](notes/cli-ux-review.md)
- [Setting up mnemos as a global brain for Claude Code — UX walkthrough](notes/global-brain-setup-ux.md)
- [Making the brain better — research synthesis](notes/RESEARCH-brain-enhancements.md)
- [Toward a perfect agent brain — research synthesis, part 2](notes/RESEARCH-perfect-agent-brain.md)
- [Brainstorm: a central brain that also holds repo-isolated knowledge](notes/design/central-plus-repo-brain.md)
- [Show HN draft — Mnemos](notes/launch/hn-show.md)
- [LinkedIn launch draft — Mnemos](notes/launch/linkedin.md)
