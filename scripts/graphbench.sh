#!/usr/bin/env bash
# Run the relational-vs-graph experiment (ADR 0043): start an ephemeral
# Postgres and, per corpus, a fresh Neo4j Community container, run
# tools/graphbench against SQLite, Postgres and Neo4j, then remove every
# container and its volumes.
#
# Usage: scripts/graphbench.sh [out-dir]
#
#   GRAPHBENCH_SIZES     corpus sizes            (default "10000 100000")
#   GRAPHBENCH_PROFILES  corpus profiles         (default "dense uniform")
#   GRAPHBENCH_ARGS      extra graphbench flags  (e.g. "-iters 200 -max-load 6")
#
# Containers follow scripts/test-integration.sh: per-run names, a label with
# the owning PID, orphan reaping, host ports assigned by Docker, and
# `docker rm -f -v` so no anonymous volume is left behind.

set -euo pipefail

OUT_DIR="${1:-docs/eval/graphbench}"
SIZES="${GRAPHBENCH_SIZES:-10000 100000}"
PROFILES="${GRAPHBENCH_PROFILES:-dense uniform}"
EXTRA_ARGS="${GRAPHBENCH_ARGS:-}"

PG_IMAGE="postgres:16-alpine"
NEO4J_IMAGE="neo4j:5.26.16-community"

RUN_ID="$$-${RANDOM}"
LABEL="mnemos.graphbench.owner"
PG_NAME="mnemos-gb-pg-${RUN_ID}"
NEO4J_NAME="mnemos-gb-neo4j-${RUN_ID}"

host_port() {
  docker port "$1" "$2/tcp" | head -1 | sed 's/.*://'
}

cleanup() {
  docker rm -f -v "${PG_NAME}" "${NEO4J_NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

reap_orphans() {
  local id owner
  docker ps -aq --filter "label=${LABEL}" | while read -r id; do
    owner="$(docker inspect -f "{{ index .Config.Labels \"${LABEL}\" }}" "${id}" 2>/dev/null || true)"
    if [[ -n "${owner}" ]] && ! kill -0 "${owner}" 2>/dev/null; then
      echo "removing orphaned graphbench container ${id} (owner ${owner} exited)"
      docker rm -f -v "${id}" >/dev/null 2>&1 || true
    fi
  done
}
reap_orphans

mkdir -p "${OUT_DIR}"
BIN="$(mktemp -d)/graphbench"
go build -o "${BIN}" ./tools/graphbench

# A random password per run: the container is throwaway, and a fixed
# credential in a committed DSN is exactly what the secret scan looks for.
PG_PASSWORD="$(openssl rand -hex 16)"
docker run -d --rm --name "${PG_NAME}" --label "${LABEL}=$$" \
  -e POSTGRES_USER=mnemos -e POSTGRES_PASSWORD="${PG_PASSWORD}" -e POSTGRES_DB=mnemos \
  -p "127.0.0.1::5432" "${PG_IMAGE}" >/dev/null
PG_PORT="$(host_port "${PG_NAME}" 5432)"
echo "started postgres on :${PG_PORT}"
for _ in $(seq 1 60); do
  if docker exec "${PG_NAME}" pg_isready -U mnemos -q 2>/dev/null; then break; fi
  sleep 1
done
PG_DSN="postgres://mnemos:${PG_PASSWORD}@127.0.0.1:${PG_PORT}/mnemos?sslmode=disable"

for profile in ${PROFILES}; do
  for size in ${SIZES}; do
    # A fresh Neo4j per corpus: Community has one user database, and loading
    # into an empty store is the only way to measure load and footprint.
    docker rm -f -v "${NEO4J_NAME}" >/dev/null 2>&1 || true
    docker run -d --rm --name "${NEO4J_NAME}" --label "${LABEL}=$$" \
      -e NEO4J_AUTH=none \
      -e NEO4J_PLUGINS='["apoc"]' \
      -e NEO4J_db_checkpoint_interval_time=10s \
      -e NEO4J_server_memory_heap_initial__size=1G \
      -e NEO4J_server_memory_heap_max__size=1G \
      -e NEO4J_server_memory_pagecache_size=1G \
      -p "127.0.0.1::7474" "${NEO4J_IMAGE}" >/dev/null
    NEO4J_PORT="$(host_port "${NEO4J_NAME}" 7474)"
    echo "== ${profile}/${size}: neo4j on :${NEO4J_PORT}"
    # shellcheck disable=SC2086
    "${BIN}" -beliefs "${size}" -profile "${profile}" \
      -pg "${PG_DSN}" -pg-container "${PG_NAME}" -pg-image "${PG_IMAGE}" \
      -neo4j "http://127.0.0.1:${NEO4J_PORT}" -neo4j-container "${NEO4J_NAME}" -neo4j-image "${NEO4J_IMAGE}" \
      -json "${OUT_DIR}/${profile}-${size}.json" ${EXTRA_ARGS}
    docker rm -f -v "${NEO4J_NAME}" >/dev/null 2>&1 || true
  done
done
