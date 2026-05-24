-- Mnemos schema for Postgres backends. Mirrors sql/sqlite/schema.sql
-- with the SQLite-specific bits (FTS5 virtual tables, json_extract,
-- BLOB embeddings) replaced by Postgres equivalents:
--
--   * timestamps:  TEXT in SQLite → timestamptz in Postgres
--   * BLOB         → bytea
--   * INTEGER PK AUTOINCREMENT → BIGSERIAL
--   * json_extract → JSON operators on jsonb columns
--   * FTS5         → tsvector + GIN index (added once the
--                    Postgres provider implements ports.TextSearcher)
--   * vss          → pgvector (optional capability, build-tagged)
--
-- The schema is namespaced via Postgres SCHEMA: the provider runs
-- `CREATE SCHEMA IF NOT EXISTS <namespace>` and `SET search_path TO
-- <namespace>` before applying this file, so unqualified table names
-- here land inside the configured namespace.
--
-- This file is the contract for migrations 000_init.sql onward;
-- destructive changes must ship as numbered up/down migrations once
-- the provider is in production.

CREATE TABLE IF NOT EXISTS events (
  id              text        PRIMARY KEY,
  run_id          text        NOT NULL,
  schema_version  text        NOT NULL,
  content         text        NOT NULL,
  source_input_id text        NOT NULL,
  timestamp       timestamptz NOT NULL,
  metadata_json   jsonb       NOT NULL,
  ingested_at     timestamptz NOT NULL,
  created_by      text        NOT NULL DEFAULT '<system>'
);

CREATE INDEX IF NOT EXISTS idx_events_timestamp       ON events(timestamp);
CREATE INDEX IF NOT EXISTS idx_events_source_input_id ON events(source_input_id);
CREATE INDEX IF NOT EXISTS idx_events_run_id          ON events(run_id);

CREATE TABLE IF NOT EXISTS claims (
  id             text             PRIMARY KEY,
  text           text             NOT NULL,
  type           text             NOT NULL,
  confidence     double precision NOT NULL,
  status         text             NOT NULL,
  created_at     timestamptz      NOT NULL,
  created_by     text             NOT NULL DEFAULT '<system>',
  trust_score    double precision NOT NULL DEFAULT 0,
  valid_from     timestamptz,
  valid_to       timestamptz,
  last_verified  timestamptz,
  verify_count   integer          NOT NULL DEFAULT 0,
  half_life_days double precision NOT NULL DEFAULT 0,
  scope_service  text             NOT NULL DEFAULT '',
  scope_env      text             NOT NULL DEFAULT '',
  scope_team     text             NOT NULL DEFAULT ''
);

-- Idempotent column adds for pre-existing namespaces upgraded from
-- earlier schema generations.
ALTER TABLE claims ADD COLUMN IF NOT EXISTS last_verified  timestamptz;
ALTER TABLE claims ADD COLUMN IF NOT EXISTS verify_count   integer          NOT NULL DEFAULT 0;
ALTER TABLE claims ADD COLUMN IF NOT EXISTS half_life_days double precision NOT NULL DEFAULT 0;
ALTER TABLE claims ADD COLUMN IF NOT EXISTS scope_service  text             NOT NULL DEFAULT '';
ALTER TABLE claims ADD COLUMN IF NOT EXISTS scope_env      text             NOT NULL DEFAULT '';
ALTER TABLE claims ADD COLUMN IF NOT EXISTS scope_team     text             NOT NULL DEFAULT '';
ALTER TABLE claims ADD COLUMN IF NOT EXISTS confidence_components jsonb NOT NULL DEFAULT '{}'::jsonb;
CREATE INDEX IF NOT EXISTS idx_claims_scope_service ON claims(scope_service);

CREATE INDEX IF NOT EXISTS idx_claims_trust_score ON claims(trust_score);
CREATE INDEX IF NOT EXISTS idx_claims_valid_to    ON claims(valid_to);

CREATE TABLE IF NOT EXISTS entities (
  id              text        PRIMARY KEY,
  name            text        NOT NULL,
  normalized_name text        NOT NULL,
  type            text        NOT NULL,
  created_at      timestamptz NOT NULL,
  created_by      text        NOT NULL DEFAULT '<system>',
  UNIQUE(normalized_name, type)
);

CREATE INDEX IF NOT EXISTS idx_entities_normalized_name ON entities(normalized_name);
CREATE INDEX IF NOT EXISTS idx_entities_type            ON entities(type);

CREATE TABLE IF NOT EXISTS claim_entities (
  claim_id  text NOT NULL REFERENCES claims(id),
  entity_id text NOT NULL REFERENCES entities(id),
  role      text NOT NULL DEFAULT 'mention',
  PRIMARY KEY (claim_id, entity_id, role)
);

CREATE INDEX IF NOT EXISTS idx_claim_entities_entity_id ON claim_entities(entity_id);

CREATE TABLE IF NOT EXISTS claim_evidence (
  claim_id text NOT NULL REFERENCES claims(id),
  event_id text NOT NULL REFERENCES events(id),
  PRIMARY KEY (claim_id, event_id)
);

CREATE INDEX IF NOT EXISTS idx_claim_evidence_event_id ON claim_evidence(event_id);

CREATE TABLE IF NOT EXISTS relationships (
  id            text        PRIMARY KEY,
  type          text        NOT NULL,
  from_claim_id text        NOT NULL REFERENCES claims(id),
  to_claim_id   text        NOT NULL REFERENCES claims(id),
  created_at    timestamptz NOT NULL,
  created_by    text        NOT NULL DEFAULT '<system>'
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_relationships_unique_edge
  ON relationships(type, from_claim_id, to_claim_id);
CREATE INDEX IF NOT EXISTS idx_relationships_from_claim ON relationships(from_claim_id);
CREATE INDEX IF NOT EXISTS idx_relationships_to_claim   ON relationships(to_claim_id);

CREATE TABLE IF NOT EXISTS compilation_jobs (
  id         text        PRIMARY KEY,
  kind       text        NOT NULL,
  status     text        NOT NULL,
  scope_json jsonb       NOT NULL,
  started_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  error      text        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_compilation_jobs_kind   ON compilation_jobs(kind);
CREATE INDEX IF NOT EXISTS idx_compilation_jobs_status ON compilation_jobs(status);

CREATE TABLE IF NOT EXISTS claim_status_history (
  id          bigserial   PRIMARY KEY,
  claim_id    text        NOT NULL REFERENCES claims(id),
  from_status text        NOT NULL,
  to_status   text        NOT NULL,
  changed_at  timestamptz NOT NULL,
  reason      text        NOT NULL,
  changed_by  text        NOT NULL DEFAULT '<system>'
);

CREATE INDEX IF NOT EXISTS idx_claim_status_history_claim_id   ON claim_status_history(claim_id);
CREATE INDEX IF NOT EXISTS idx_claim_status_history_changed_at ON claim_status_history(changed_at);

-- Embeddings: bytea matches the SQLite BLOB shape today. Once the
-- Postgres provider gains a pgvector capability path, embeddings
-- will live in a parallel `embeddings_pgvector` table with a
-- vector(N) column; the bytea column stays for portability.
CREATE TABLE IF NOT EXISTS embeddings (
  entity_id   text             NOT NULL,
  entity_type text             NOT NULL,
  vector      bytea            NOT NULL,
  model       text             NOT NULL,
  dimensions  integer          NOT NULL,
  created_at  timestamptz      NOT NULL,
  created_by  text             NOT NULL DEFAULT '<system>',
  PRIMARY KEY (entity_id, entity_type)
);

CREATE INDEX IF NOT EXISTS idx_embeddings_entity_type ON embeddings(entity_type);

CREATE TABLE IF NOT EXISTS users (
  id          text        PRIMARY KEY,
  name        text        NOT NULL,
  email       text        NOT NULL UNIQUE,
  status      text        NOT NULL DEFAULT 'active',
  scopes_json jsonb       NOT NULL DEFAULT '["*"]'::jsonb,
  created_at  timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_users_status ON users(status);

CREATE TABLE IF NOT EXISTS revoked_tokens (
  jti        text        PRIMARY KEY,
  revoked_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_revoked_tokens_expires_at ON revoked_tokens(expires_at);

CREATE TABLE IF NOT EXISTS agents (
  id                 text        PRIMARY KEY,
  name               text        NOT NULL,
  owner_id           text        NOT NULL REFERENCES users(id),
  scopes_json        jsonb       NOT NULL DEFAULT '[]'::jsonb,
  allowed_runs_json  jsonb       NOT NULL DEFAULT '[]'::jsonb,
  status             text        NOT NULL DEFAULT 'active',
  created_at         timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_agents_owner_id ON agents(owner_id);
CREATE INDEX IF NOT EXISTS idx_agents_status   ON agents(status);

CREATE TABLE IF NOT EXISTS actions (
  id            text        PRIMARY KEY,
  run_id        text        NOT NULL DEFAULT '',
  kind          text        NOT NULL,
  subject       text        NOT NULL,
  actor         text        NOT NULL DEFAULT '',
  at            timestamptz NOT NULL,
  metadata_json jsonb       NOT NULL DEFAULT '{}'::jsonb,
  created_by    text        NOT NULL DEFAULT '<system>',
  created_at    timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_actions_run_id  ON actions(run_id);
CREATE INDEX IF NOT EXISTS idx_actions_subject ON actions(subject);
CREATE INDEX IF NOT EXISTS idx_actions_kind    ON actions(kind);
CREATE INDEX IF NOT EXISTS idx_actions_at      ON actions(at);

CREATE TABLE IF NOT EXISTS outcomes (
  id           text        PRIMARY KEY,
  action_id    text        NOT NULL REFERENCES actions(id),
  result       text        NOT NULL,
  metrics_json jsonb       NOT NULL DEFAULT '{}'::jsonb,
  notes        text        NOT NULL DEFAULT '',
  observed_at  timestamptz NOT NULL,
  source       text        NOT NULL DEFAULT 'push',
  created_by   text        NOT NULL DEFAULT '<system>',
  created_at   timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_outcomes_action_id   ON outcomes(action_id);
CREATE INDEX IF NOT EXISTS idx_outcomes_result      ON outcomes(result);
CREATE INDEX IF NOT EXISTS idx_outcomes_observed_at ON outcomes(observed_at);

CREATE TABLE IF NOT EXISTS lessons (
  id            text        PRIMARY KEY,
  statement     text        NOT NULL,
  scope_service text        NOT NULL DEFAULT '',
  scope_env     text        NOT NULL DEFAULT '',
  scope_team    text        NOT NULL DEFAULT '',
  trigger       text        NOT NULL DEFAULT '',
  kind          text        NOT NULL DEFAULT '',
  confidence    double precision NOT NULL,
  derived_at    timestamptz NOT NULL,
  last_verified timestamptz,
  source        text        NOT NULL DEFAULT 'synthesize',
  created_by    text        NOT NULL DEFAULT '<system>'
);

CREATE INDEX IF NOT EXISTS idx_lessons_scope_service ON lessons(scope_service);
CREATE INDEX IF NOT EXISTS idx_lessons_scope_env     ON lessons(scope_env);
CREATE INDEX IF NOT EXISTS idx_lessons_scope_team    ON lessons(scope_team);
CREATE INDEX IF NOT EXISTS idx_lessons_kind          ON lessons(kind);
CREATE INDEX IF NOT EXISTS idx_lessons_trigger       ON lessons(trigger);
CREATE INDEX IF NOT EXISTS idx_lessons_confidence    ON lessons(confidence);

CREATE TABLE IF NOT EXISTS lesson_evidence (
  lesson_id text NOT NULL REFERENCES lessons(id),
  action_id text NOT NULL REFERENCES actions(id),
  PRIMARY KEY (lesson_id, action_id)
);

CREATE INDEX IF NOT EXISTS idx_lesson_evidence_action_id ON lesson_evidence(action_id);

CREATE TABLE IF NOT EXISTS decisions (
  id                text        PRIMARY KEY,
  statement         text        NOT NULL,
  plan              text        NOT NULL DEFAULT '',
  reasoning         text        NOT NULL DEFAULT '',
  risk_level        text        NOT NULL,
  alternatives_json jsonb       NOT NULL DEFAULT '[]'::jsonb,
  outcome_id        text        NOT NULL DEFAULT '',
  chosen_at         timestamptz NOT NULL,
  created_by        text        NOT NULL DEFAULT '<system>',
  created_at        timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_decisions_chosen_at  ON decisions(chosen_at);
CREATE INDEX IF NOT EXISTS idx_decisions_risk_level ON decisions(risk_level);
CREATE INDEX IF NOT EXISTS idx_decisions_outcome_id ON decisions(outcome_id);

CREATE TABLE IF NOT EXISTS decision_beliefs (
  decision_id text NOT NULL REFERENCES decisions(id),
  claim_id    text NOT NULL REFERENCES claims(id),
  PRIMARY KEY (decision_id, claim_id)
);

CREATE INDEX IF NOT EXISTS idx_decision_beliefs_claim_id ON decision_beliefs(claim_id);

CREATE TABLE IF NOT EXISTS playbooks (
  id            text             PRIMARY KEY,
  trigger       text             NOT NULL,
  statement     text             NOT NULL,
  scope_service text             NOT NULL DEFAULT '',
  scope_env     text             NOT NULL DEFAULT '',
  scope_team    text             NOT NULL DEFAULT '',
  steps_json    jsonb            NOT NULL DEFAULT '[]'::jsonb,
  confidence    double precision NOT NULL,
  derived_at    timestamptz      NOT NULL,
  last_verified timestamptz,
  source        text             NOT NULL DEFAULT 'synthesize',
  created_by    text             NOT NULL DEFAULT '<system>'
);

CREATE INDEX IF NOT EXISTS idx_playbooks_trigger       ON playbooks(trigger);
CREATE INDEX IF NOT EXISTS idx_playbooks_scope_service ON playbooks(scope_service);
CREATE INDEX IF NOT EXISTS idx_playbooks_confidence    ON playbooks(confidence);

CREATE TABLE IF NOT EXISTS playbook_lessons (
  playbook_id text NOT NULL REFERENCES playbooks(id),
  lesson_id   text NOT NULL REFERENCES lessons(id),
  PRIMARY KEY (playbook_id, lesson_id)
);

CREATE INDEX IF NOT EXISTS idx_playbook_lessons_lesson_id ON playbook_lessons(lesson_id);

 ALTER TABLE decisions ADD COLUMN IF NOT EXISTS scope_service          text NOT NULL DEFAULT '';
 ALTER TABLE decisions ADD COLUMN IF NOT EXISTS scope_env              text NOT NULL DEFAULT '';
 ALTER TABLE decisions ADD COLUMN IF NOT EXISTS scope_team             text NOT NULL DEFAULT '';
 ALTER TABLE decisions ADD COLUMN IF NOT EXISTS refuted_beliefs_json   text NOT NULL DEFAULT '[]';
 ALTER TABLE decisions ADD COLUMN IF NOT EXISTS failed_outcome_id      text NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS lesson_versions (
  version_id   bigserial   PRIMARY KEY,
  lesson_id    text        NOT NULL REFERENCES lessons(id),
  payload_json jsonb       NOT NULL,
  valid_from   timestamptz NOT NULL,
  valid_to     timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_lesson_versions_lesson_id ON lesson_versions(lesson_id);

CREATE TABLE IF NOT EXISTS playbook_versions (
  version_id   bigserial   PRIMARY KEY,
  playbook_id  text        NOT NULL REFERENCES playbooks(id),
  payload_json jsonb       NOT NULL,
  valid_from   timestamptz NOT NULL,
  valid_to     timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_playbook_versions_playbook_id ON playbook_versions(playbook_id);

CREATE TABLE IF NOT EXISTS entity_relationships (
  id         text        PRIMARY KEY,
  kind       text        NOT NULL,
  from_id    text        NOT NULL,
  from_type  text        NOT NULL,
  to_id      text        NOT NULL,
  to_type    text        NOT NULL,
  created_at timestamptz NOT NULL,
  created_by text        NOT NULL DEFAULT '<system>'
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_entity_relationships_unique_edge
  ON entity_relationships(kind, from_type, from_id, to_type, to_id);
CREATE INDEX IF NOT EXISTS idx_entity_relationships_from ON entity_relationships(from_type, from_id);
CREATE INDEX IF NOT EXISTS idx_entity_relationships_to   ON entity_relationships(to_type, to_id);
