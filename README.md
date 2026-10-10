# backstop

A live guard against the Postgres lock queue death spiral.

## The problem

Every schema change takes `ACCESS EXCLUSIVE` on its table, which conflicts with
everything, including plain `SELECT`s. Postgres lock queues are fair, so a
waiting `ALTER` becomes a cork: every read that arrives after it queues behind
it, even though the `ALTER` is not running, it is just holding a ticket. One
migration lands next to one long-running query and the whole database's traffic
piles up behind the cork. The connection pool saturates, health checks fail,
pods restart. The migration itself takes 200ms; the outage lasts as long as the
blocker.

Migration linters are static. They read SQL and warn. They cannot see that a
safe migration is about to land next to a 90-second analytics query, and they
cannot act. The only dynamic control is a hand-written `SET lock_timeout` that
nothing enforces.

backstop watches the live lock graph, notices the queue building behind a cork,
and cancels the corking statement before the queue exhausts the pool.

## How it works

    [Postgres]
        ^ sql poll (~1s)
    [Collector]  pg_locks + pg_stat_activity + pg_blocking_pids
        |
    [Detector]   pure: snapshot + thresholds -> verdict
        |
    [Guard]      rails: protected targets, cooldown, identity, root match
        |
    [Actuator]   pg_cancel_backend, re-verified against pid reuse
        |
    [Metrics]    Prometheus + structured logs

The cork is either a **pending** strong lock (a DDL statement waiting for the
lock, which is what `pg_blocking_pids` reports the readers as waiting on) or a
**holding** session that is idle in transaction. Both are detected. Only the
pending case is actionable by cancellation.

## Install

    go build -o bin/backstop ./cmd/backstop

Single static binary, no runtime. Cross-compile with
`GOOS=linux GOARCH=arm64 go build ./cmd/backstop`.

## Use

    export BACKSTOP_DATABASE_URL="postgres://user:pass@host:5432/db"

Start in observe mode, always:

    ./bin/backstop -mode observe

It logs what it would have done and nothing else. Watch it for a week. When the
verdicts look right, enable cancellation:

    ./bin/backstop -mode cancel

Flags override the config file. `-min-waiters`, `-min-wait-seconds` and
`-growth-factor` are the tuning knobs.

## Config

`config.example.json` documents every field. Key ones:

| field | default | meaning |
| --- | --- | --- |
| `mode` | `observe` | `observe` logs only; `cancel` acts |
| `poll_interval_seconds` | 1 | how often the lock graph is read |
| `min_waiters` | 5 | sessions queued behind a cork before acting |
| `min_wait_seconds` | 5 | oldest wait age before acting |
| `growth_factor` | 2.0 | queue growth multiple required; 1 disables the gate |
| `cooldown_seconds` | 60 | minimum gap between actions on one target |
| `protected_users` | `postgres`, `replication` | never act on these roles |
| `protected_apps` | `backstop`, `pgbouncer` | never act on these applications |
| `protected_pids` | `[]` | never act on these backends |

## Safety rails

Cancelling queries in production is a trust problem, so the rails are the
product:

- **Observe by default.** Cancellation is opt-in per run.
- **Cancel, never terminate.** `pg_cancel_backend` fails the statement and keeps
  the session. Termination is not implemented.
- **Root match.** Only the cork the verdict identified is ever touched, never a
  downstream waiter.
- **Identity verification.** The target's `backend_start` is re-read immediately
  before acting; if it changed, the pid was recycled and the action is dropped.
- **Protected targets.** Roles, application names and pids are excluded.
- **Cooldown.** One target cannot be acted on twice inside the window.
- **Idle-in-transaction is refused.** Cancelling its current query would not
  release its lock, so backstop declines rather than pretending to help.

Every withheld action is logged with its reasons.

## Metrics

`GET :9104/metrics`

    backstop_lock_waiters
    backstop_lock_oldest_wait_seconds
    backstop_blocked_backends
    backstop_spiral_active
    backstop_spirals_detected_total
    backstop_actions_total{outcome}
    backstop_cycles_total{result}

`GET :9104/healthz` and `GET :9104/readyz` for liveness and readiness.

## Demo

    docker compose up -d --wait
    make demo

The demo holds a read lock, queues a schema change behind it, then ramps reads
into the growing queue, and reports the blocked-session count before and after
backstop acts.

## Limits

- Cancelling needs a role with `pg_signal_backend` or ownership of the target.
- Thresholds need tuning per database; observe mode exists for that.
- Single database per process. No dashboard, no Kubernetes operator, by design.
