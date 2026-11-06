// Package db holds the Postgres code. Migrate applies the embedded schema migrations, Open connects,
// Events saves and lists the payment event log, Products stores the catalogue, and Orders stores
// orders.
package db

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/aleksiuno/solpay-shop/migrations"
)

// migrateLockWait bounds each wait for golang-migrate's advisory lock.
const migrateLockWait = 30 * time.Second

// Migrate applies every pending migration embedded in the migrations package to the database at
// databaseURL. golang-migrate holds a Postgres advisory lock while it runs, so servers that start
// at the same time apply each migration once. Its API takes no context, so the driver URL bounds
// the waits instead. connect_timeout bounds the connection, unless databaseURL sets it.
// lock_timeout, always migrateLockWait, bounds every wait for a lock, a migration's table locks
// included.
//
// golang-migrate takes the advisory lock twice: while it opens the database, to create
// schema_migrations, and again to apply the migrations. Only lock_timeout bounds the first.
// golang-migrate's own timeout also bounds the second, and is set 5 s longer, so that Postgres
// always gives up first.
func Migrate(databaseURL string) error {
	m, err := newMigrator(databaseURL, migrateLockWait)
	if err != nil {
		return err
	}
	// Close errors are ignored: every migration has committed or rolled back by now, and Close
	// only releases the connection and the embedded source.
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// newMigrator prepares golang-migrate for the database at databaseURL, with each wait for its
// advisory lock bounded by lockWait.
func newMigrator(databaseURL string, lockWait time.Duration) (*migrate.Migrate, error) {
	driverURL, err := migrateURL(databaseURL, lockWait)
	if err != nil {
		return nil, err
	}
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, driverURL)
	if err != nil {
		return nil, fmt.Errorf("prepare migrations: %w", err)
	}
	// Were golang-migrate's timer to fire first, it would leave pg_advisory_lock running, and
	// Close would block until Postgres cancelled it.
	m.LockTimeout = lockWait + 5*time.Second
	return m, nil
}

// migrateURL turns a postgres:// or postgresql:// URL into the pgx5:// URL that selects
// golang-migrate's pgx v5 driver. It sets lock_timeout to lockWait, and connect_timeout when it is
// missing.
func migrateURL(databaseURL string, lockWait time.Duration) (string, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		// A *url.Error repeats the whole URL, password included.
		return "", errors.New("database URL is not a valid URL")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" { // url.Parse lowercases the scheme
		return "", fmt.Errorf("database URL scheme %q: want postgres or postgresql", u.Scheme)
	}
	u.Scheme = "pgx5"
	q := u.Query()
	if !q.Has("connect_timeout") {
		q.Set("connect_timeout", "10")
	}
	// A lock_timeout in databaseURL is replaced rather than read: golang-migrate's own timeout must
	// stay longer, and Postgres accepts forms such as 1min that time.ParseDuration does not. The
	// value is written in milliseconds, since Duration.String writes a minute as 1m0s, which
	// Postgres rejects.
	q.Set("lock_timeout", fmt.Sprintf("%dms", lockWait.Milliseconds()))
	u.RawQuery = q.Encode()
	return u.String(), nil
}
