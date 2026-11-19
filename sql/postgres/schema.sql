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
  id          text             PRIMARY KEY,
  text        text             NOT NULL,
  type        text             NOT NULL,
  confidence  double precision NOT NULL,
  status      text             NOT NULL,
  created_at  timestamptz      NOT NULL,
  created_by  text             NOT NULL DEFAULT '<system>',
  trust_score double precision NOT NULL DEFAULT 0,
  valid_from  timestamptz,
  valid_to    timestamptz
);

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
CREATE INDEX IF NOT EXISTS idx_relationships_type_created ON relationships(type, created_at, id);

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
