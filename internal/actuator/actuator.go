package actuator

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Result struct {
	PID   int32
	Query string
	Sent  bool
	At    time.Time
	Err   error
}

type Actuator struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Actuator {
	return &Actuator{pool: pool}
}

func (a *Actuator) Cancel(ctx context.Context, pid int32) (Result, error) {
	var ok bool
	if err := a.pool.QueryRow(ctx, "SELECT pg_cancel_backend($1)", pid).Scan(&ok); err != nil {
		return Result{PID: pid, At: time.Now(), Err: err}, fmt.Errorf("cancel backend %d: %w", pid, err)
	}
	if !ok {
		return Result{PID: pid, At: time.Now(), Err: fmt.Errorf("backend %d rejected cancellation", pid)}, fmt.Errorf("backend %d rejected cancellation", pid)
	}
	return Result{PID: pid, Sent: true, At: time.Now()}, nil
}
