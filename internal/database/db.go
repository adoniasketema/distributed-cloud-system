package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultMaxConns is the pool size used when none is configured.
//
// Uploads take a connection per chunk write, so the pool sets the ceiling on concurrent
// uploads before requests start queueing behind them. The old value of 10 was low enough
// that a handful of concurrent uploads could stall unrelated traffic such as logins.
const DefaultMaxConns int32 = 25

// New connects to the PostgreSQL database using a connection pool.
// A maxConns of 0 or less selects DefaultMaxConns.
func New(dsn string, maxConns int32) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if maxConns <= 0 {
		maxConns = DefaultMaxConns
	}

	// Configure pool settings
	config.MaxConns = maxConns
	config.MinConns = 2
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 30 * time.Minute

	// Connect
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Ping to ensure connection is valid
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return pool, nil
}
