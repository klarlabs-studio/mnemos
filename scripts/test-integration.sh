#!/usr/bin/env bash
# Spin up ephemeral postgres + mysql containers, run the gated
# integration suites against them, then tear the containers down.
# Mirrors the GitHub Actions database-providers job for local
# reproduction.
#
# Usage: make test-integration
#
# Containers use uncommon ports (55432 / 53306) to avoid colliding
# with developer-local servers. They are auto-removed on stop
# (--rm) and on script exit.

set -euo pipefail

PG_NAME="mnemos-pg-itest"
MY_NAME="mnemos-my-itest"
PG_PORT=55432
MY_PORT=53306
PG_DSN="postgres://mnemos:mnemos@127.0.0.1:${PG_PORT}/mnemos?sslmode=disable"
MY_DSN="mysql://root:mnemos@127.0.0.1:${MY_PORT}/"

cleanup() {
  docker stop "${PG_NAME}" >/dev/null 2>&1 || true
  docker stop "${MY_NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "starting postgres on :${PG_PORT}…"
docker run -d --rm --name "${PG_NAME}" \
  -e POSTGRES_USER=mnemos -e POSTGRES_PASSWORD=mnemos -e POSTGRES_DB=mnemos \
  -p "127.0.0.1:${PG_PORT}:5432" \
  postgres:16-alpine >/dev/null

echo "starting mysql on :${MY_PORT}…"
docker run -d --rm --name "${MY_NAME}" \
  -e MYSQL_ROOT_PASSWORD=mnemos -e MYSQL_DATABASE=mnemos \
  -p "127.0.0.1:${MY_PORT}:3306" \
  mysql:8 >/dev/null

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
TEST_POSTGRES_DSN="${PG_DSN}" \
TEST_MYSQL_DSN="${MY_DSN}" \
  go test -race -count=1 ./internal/store/postgres/ ./internal/store/mysql/ ./internal/store/
# ./internal/store/ holds the cross-backend suites (trust invariance,
# DeleteByIDs, ...): without a live Postgres and MySQL they run on memory and
# SQLite only, which is how they ran everywhere before this line.
