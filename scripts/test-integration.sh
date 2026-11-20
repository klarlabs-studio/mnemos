#!/usr/bin/env bash
# Spin up ephemeral postgres + mysql containers, run the gated
# integration suites against them, then tear the containers down.
# Mirrors the GitHub Actions database-providers job for local
# reproduction.
#
# Usage: make test-integration
#
# Docker assigns each container a free host port, read back with
# `docker port`. Fixed ports (55432 / 53306) collided with any other local
# project using the same ones, and the gate failed on "port is already
# allocated" for reasons unrelated to the change under test. Set
# MNEMOS_ITEST_PG_PORT / MNEMOS_ITEST_MY_PORT to pin them. Containers are
# auto-removed on stop (--rm) and on script exit.
#
# Container names are per run for the same reason. Fixed names
# (mnemos-pg-itest / mnemos-my-itest) meant a gate killed before its EXIT trap
# ran left containers behind that failed every later gate with "name already
# in use", and two gates in different worktrees could never run at once. Each
# container is labelled with the PID of the run that owns it; a run removes
# only labelled containers whose owner process no longer exists, so it never
# touches a concurrent run's databases.

set -euo pipefail

RUN_ID="$$-${RANDOM}"
LABEL="mnemos.itest.owner"
PG_NAME="mnemos-pg-itest-${RUN_ID}"
MY_NAME="mnemos-my-itest-${RUN_ID}"
PG_PORT="${MNEMOS_ITEST_PG_PORT:-}"
MY_PORT="${MNEMOS_ITEST_MY_PORT:-}"

# host_port NAME CONTAINER_PORT prints the host port docker bound to it.
host_port() {
  docker port "$1" "$2/tcp" | head -1 | sed 's/.*://'
}

# -v: `docker rm -f` overrides --rm and would leave each container's anonymous
# data volume behind (~1.3 GB per run), which filled the disk.
cleanup() {
  docker rm -f -v "${PG_NAME}" "${MY_NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

# reap_orphans removes containers left by runs whose process is gone.
reap_orphans() {
  local id owner
  docker ps -aq --filter "label=${LABEL}" | while read -r id; do
    owner="$(docker inspect -f "{{ index .Config.Labels \"${LABEL}\" }}" "${id}" 2>/dev/null || true)"
    if [[ -n "${owner}" ]] && ! kill -0 "${owner}" 2>/dev/null; then
      echo "removing orphaned integration container ${id} (owner ${owner} exited)"
      docker rm -f -v "${id}" >/dev/null 2>&1 || true
    fi
  done
}
reap_orphans

docker run -d --rm --name "${PG_NAME}" --label "${LABEL}=$$" \
  -e POSTGRES_USER=mnemos -e POSTGRES_PASSWORD=mnemos -e POSTGRES_DB=mnemos \
  -p "127.0.0.1:${PG_PORT}:5432" \
  postgres:16-alpine >/dev/null
PG_PORT="$(host_port "${PG_NAME}" 5432)"
echo "started postgres on :${PG_PORT}"

docker run -d --rm --name "${MY_NAME}" --label "${LABEL}=$$" \
  -e MYSQL_ROOT_PASSWORD=mnemos -e MYSQL_DATABASE=mnemos \
  -p "127.0.0.1:${MY_PORT}:3306" \
  mysql:8 >/dev/null
MY_PORT="$(host_port "${MY_NAME}" 3306)"
echo "started mysql on :${MY_PORT}"

PG_DSN="postgres://mnemos:mnemos@127.0.0.1:${PG_PORT}/mnemos?sslmode=disable"
MY_DSN="mysql://root:mnemos@127.0.0.1:${MY_PORT}/"

echo "waiting for postgres…"
for i in $(seq 1 60); do
  if docker exec "${PG_NAME}" pg_isready -U mnemos -q 2>/dev/null; then break; fi
  sleep 1
done

echo "waiting for mysql…"
for i in $(seq 1 120); do
  if docker exec "${MY_NAME}" mysqladmin ping -h 127.0.0.1 --silent 2>/dev/null; then break; fi
  sleep 1
done

echo "running integration tests…"
# -timeout matches the test step in .warden.yaml: the cross-backend suite in
# ./internal/store/ ran past Go's 10m default on a loaded machine.
TEST_POSTGRES_DSN="${PG_DSN}" \
TEST_MYSQL_DSN="${MY_DSN}" \
  go test -race -count=1 -timeout 30m ./internal/store/postgres/ ./internal/store/mysql/ ./internal/store/ ./internal/govwrite/
# ./internal/store/ holds the cross-backend suites (trust invariance,
# DeleteByIDs, ...): without a live Postgres and MySQL they run on memory and
# SQLite only, which is how they ran everywhere before this line.
# ./internal/govwrite/ holds the id-addressed belief suite (ADR 0041), which
# asserts redirects and folds on what each backend returns.
