#!/usr/bin/env bash
set -euo pipefail

PSQL=(docker exec -i backstop-postgres psql -U app -d backstop -X -q -A -F ' | ')
q() { "${PSQL[@]}" -c "$1"; }

q "DROP TABLE IF EXISTS users; CREATE TABLE users (id int primary key, name text);"
q "INSERT INTO users VALUES (1,'a'),(2,'b');"

echo "== scenario: reader holds, DDL queues, reads ramp =="
q "BEGIN; SELECT count(*) FROM users; SELECT pg_sleep(14); COMMIT;" >/dev/null &
sleep 1
q "ALTER TABLE users ADD COLUMN last_seen timestamptz;" >/dev/null &
sleep 1
for _ in $(seq 1 4); do q "SELECT * FROM users;" >/dev/null & done
sleep 3
for _ in $(seq 1 8); do q "SELECT * FROM users;" >/dev/null & done

echo
for t in 1 2 3 4; do
  echo "-- t+${t}s: pid | state | wait | blocking | left(query,34)"
  q "SELECT a.pid, coalesce(a.state,''), coalesce(a.wait_event_type,''), pg_blocking_pids(a.pid)::text,
            left(regexp_replace(coalesce(a.query,''),'\s+',' ','g'),34)
     FROM pg_stat_activity a
     WHERE a.pid <> pg_backend_pid() AND a.datname='backstop'
     ORDER BY a.pid;"
  echo "-- t+${t}s: relation users locks (pid | mode | granted)"
  q "SELECT l.pid, l.mode, l.granted FROM pg_locks l JOIN pg_class c ON c.oid=l.relation
     WHERE c.relname='users' ORDER BY l.granted, l.pid;"
  echo
  sleep 1
done

wait || true
