package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultMaxConns is the pool size used when the caller asks for nothing.
//
// pgx's own default is max(4, NumCPU) — four on the t4g.small this runs on, for
// a pool shared by river's LISTEN/NOTIFY notifier (which holds one connection
// open indefinitely), its elector, producer and completer, five job workers,
// and every HTTP request. Exhaustion does not surface as an error: callers
// queue inside Acquire until their own context expires, so it presents as slow
// requests, and for a scan as a job that spends its budget waiting for a
// connection rather than searching.
const DefaultMaxConns = 20

// minIdleConns keeps a couple of connections warm. The database is Neon, across
// the public internet rather than inside the VPC, so a cold acquire pays DNS,
// TCP, TLS and Postgres startup on whatever request happens to want it.
const minIdleConns = 2

// poolStatsInterval is how often the pool's saturation is logged.
const poolStatsInterval = 60 * time.Second

// Connect creates a pgx connection pool from a Postgres connection string.
//
// Sizing is applied here rather than left to the URL's query parameters. The
// connection string is operator-supplied and, for Neon, pasted out of a
// console; a pool too small to run the job runner and serve requests at the
// same time would otherwise be one missing parameter away, and its symptom is
// latency rather than an error.
func Connect(ctx context.Context, url string, maxConns int) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.ParseConfig: %w", err)
	}
	if maxConns <= 0 {
		maxConns = DefaultMaxConns
	}
	cfg.MaxConns = int32(maxConns)
	cfg.MinConns = minIdleConns
	if cfg.MinConns > cfg.MaxConns {
		// pgxpool refuses to build a config whose floor is above its ceiling,
		// and a deliberately tiny pool (a one-off script, a test) is a legal
		// thing to ask for.
		cfg.MinConns = cfg.MaxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.NewWithConfig: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db ping: %w", err)
	}
	return pool, nil
}

// StartPoolStatsLogger reports pool saturation every minute until ctx is done.
//
// Nothing else can tell you the pool is the bottleneck. A request blocked in
// Acquire looks exactly like a slow query from the outside, and a scan that
// spent its budget queueing looks like a slow upstream. EmptyAcquireCount and
// EmptyAcquireWaitTime are the two numbers that separate those cases.
//
// The caller owns the context and must cancel it before closing the pool —
// otherwise this goroutine outlives what it is reporting on.
func StartPoolStatsLogger(ctx context.Context, pool *pgxpool.Pool) {
	go func() {
		ticker := time.NewTicker(poolStatsInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s := pool.Stat()
				slog.Info("db pool",
					"acquired", s.AcquiredConns(),
					"idle", s.IdleConns(),
					"total", s.TotalConns(),
					"max", s.MaxConns(),
					"empty_acquires", s.EmptyAcquireCount(),
					"empty_acquire_wait", s.EmptyAcquireWaitTime().String(),
					"canceled_acquires", s.CanceledAcquireCount(),
				)
			}
		}
	}()
}
