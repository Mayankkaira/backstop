package pgstat

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const backendsQuery = `
SELECT
	a.pid,
	coalesce(a.usename, ''),
	coalesce(a.application_name, ''),
	coalesce(a.state, ''),
	coalesce(a.wait_event_type, ''),
	coalesce(a.wait_event, ''),
	coalesce(a.query, ''),
	a.query_start,
	a.xact_start,
	a.backend_start,
	coalesce(a.client_addr::text, ''),
	pg_blocking_pids(a.pid)
FROM pg_stat_activity a
WHERE a.pid <> pg_backend_pid()`

const locksQuery = `
SELECT
	l.pid,
	coalesce(l.locktype, ''),
	coalesce(l.mode, ''),
	coalesce(l.relation, 0),
	l.granted,
	coalesce(c.relname, ''),
	coalesce(n.nspname, '')
FROM pg_locks l
LEFT JOIN pg_class c ON c.oid = l.relation
LEFT JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE l.pid IS NOT NULL AND l.pid <> pg_backend_pid()`

const identityQuery = `SELECT backend_start FROM pg_stat_activity WHERE pid = $1`

type Collector struct {
	pool *pgxpool.Pool
}

func NewCollector(pool *pgxpool.Pool) *Collector {
	return &Collector{pool: pool}
}

func (c *Collector) Snapshot(ctx context.Context) (Snapshot, error) {
	at := time.Now()
	backends, err := c.backends(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("collect backends: %w", err)
	}
	held, pending, err := c.locks(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("collect locks: %w", err)
	}
	return Snapshot{At: at, Backends: backends, Held: held, Pending: pending}, nil
}

func (c *Collector) BackendIdentity(ctx context.Context, pid int32) (*time.Time, error) {
	var started *time.Time
	if err := c.pool.QueryRow(ctx, identityQuery, pid).Scan(&started); err != nil {
		return nil, fmt.Errorf("read backend identity for pid %d: %w", pid, err)
	}
	return started, nil
}

func (c *Collector) backends(ctx context.Context) (map[int32]Backend, error) {
	rows, err := c.pool.Query(ctx, backendsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int32]Backend)
	for rows.Next() {
		var b Backend
		if err := rows.Scan(
			&b.PID,
			&b.User,
			&b.ApplicationName,
			&b.State,
			&b.WaitEventType,
			&b.WaitEvent,
			&b.Query,
			&b.QueryStart,
			&b.XactStart,
			&b.BackendStart,
			&b.ClientAddr,
			&b.Blockers,
		); err != nil {
			return nil, err
		}
		out[b.PID] = b
	}
	return out, rows.Err()
}

func (c *Collector) locks(ctx context.Context) ([]Lock, []Lock, error) {
	rows, err := c.pool.Query(ctx, locksQuery)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var held, pending []Lock
	for rows.Next() {
		var l Lock
		var granted bool
		if err := rows.Scan(
			&l.PID,
			&l.LockType,
			&l.Mode,
			&l.Relation,
			&granted,
			&l.RelationName,
			&l.SchemaName,
		); err != nil {
			return nil, nil, err
		}
		if granted {
			held = append(held, l)
		} else {
			pending = append(pending, l)
		}
	}
	return held, pending, rows.Err()
}
