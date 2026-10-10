#!/usr/bin/env bash
set -euo pipefail

DSN="${BACKSTOP_DATABASE_URL:-postgres://app:app@127.0.0.1:55432/backstop}"
BIN="${BIN:-./bin/backstop}"
MODE="${MODE:-observe}"
HOLD="${HOLD:-14}"

if command -v psql >/dev/null 2>&1; then
  PSQL=(psql "$DSN")
elif docker ps --format '{{.Names}}' | grep -q '^backstop-postgres$'; then
  PSQL=(docker exec -i backstop-postgres psql -U app -d backstop)
else
  echo "no psql available (install postgresql-client or run: docker compose up -d)" >&2
  exit 1
fi

q() { "${PSQL[@]}" -q -c "$1"; }

echo "== backstop demo =="
echo "dsn:  $DSN"
echo "mode: $MODE"

q "DROP TABLE IF EXISTS users; CREATE TABLE users (id int primary key, name text);"
q "INSERT INTO users VALUES (1,'a'),(2,'b');"

echo
echo "-- 1. a long analytics query holds a read lock on users for ${HOLD}s"
q "BEGIN; SELECT count(*) FROM users; SELECT pg_sleep($HOLD); COMMIT;" >/dev/null &

sleep 1

echo "-- 2. a schema change wants to rewrite the table; it queues for an exclusive lock"
q "ALTER TABLE users ADD COLUMN last_seen timestamptz;" >/dev/null &

sleep 1

echo "-- 3. the queue starts building: a few reads pile up behind the schema change"
for _ in $(seq 1 4); do q "SELECT * FROM users;" >/dev/null & done

sleep 3

echo "-- 4. traffic keeps arriving, the queue deepens"
for _ in $(seq 1 8); do q "SELECT * FROM users;" >/dev/null & done

sleep 1
echo
echo "-- 5. backstop runs against the live queue for 8s"
WAITERS_BEFORE=$(docker exec -i backstop-postgres psql -U app -d backstop -X -q -A -t -c \
  "SELECT count(*) FROM pg_stat_activity WHERE datname='backstop' AND cardinality(pg_blocking_pids(pid))>0;" 2>/dev/null | tr -d '[:space:]')
echo "   queue depth before backstop acts: ${WAITERS_BEFORE} blocked sessions"

timeout 8 "$BIN" -database-url "$DSN" -mode "$MODE" \
  -min-waiters "${MIN_WAITERS:-5}" \
  -min-wait-seconds "${MIN_WAIT_SECONDS:-3}" \
  -growth-factor "${GROWTH_FACTOR:-1.5}" || true

sleep 3
WAITERS_AFTER=$(docker exec -i backstop-postgres psql -U app -d backstop -X -q -A -t -c \
  "SELECT count(*) FROM pg_stat_activity WHERE datname='backstop' AND cardinality(pg_blocking_pids(pid))>0;" 2>/dev/null | tr -d '[:space:]')
echo "   queue depth after:               ${WAITERS_AFTER} blocked sessions"

wait || true
echo
echo "== demo done =="
