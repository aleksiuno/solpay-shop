package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aleksiuno/solpay-shop/internal/db/sqlcdb"
)

// pingTimeout bounds the round trip with which Open checks the database.
const pingTimeout = 10 * time.Second

// Open returns a connection pool for the database at databaseURL, once a round trip has shown that
// the database answers.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// pgx hides the password in its parse errors only on a best-effort basis.
		return nil, errors.New("database URL is not a valid URL")
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to the database: %w", err)
	}
	return pool, nil
}

// inTx runs fn in one transaction, with queries bound to it, at the default READ COMMITTED level.
// The transaction commits if fn returns nil and rolls back if it returns an error.
func inTx(ctx context.Context, pool *pgxpool.Pool, fn func(q *sqlcdb.Queries) error) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return fn(sqlcdb.New(tx))
	})
}
