// Package db holds the Postgres code. Migrate applies the embedded schema migrations, Open connects,
// Events saves and lists the payment event log, Products stores the catalogue, and Orders stores
// orders.
package db

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/aleksiuno/solpay-shop/migrations"
)

// Migrate applies every pending migration embedded in the migrations package to the database at
// databaseURL. golang-migrate holds a Postgres advisory lock while it runs, so servers that start
// at the same time apply each migration once. Its API takes no context, so the URL bounds the two
// waits instead: connect_timeout for the connection and lock_timeout for the lock, unless
// databaseURL already sets them.
func Migrate(databaseURL string) error {
	driverURL, err := migrateURL(databaseURL)
	if err != nil {
		return err
	}
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, driverURL)
	if err != nil {
		return fmt.Errorf("prepare migrations: %w", err)
	}
	// Close errors are ignored: every migration has committed or rolled back by now, and Close
	// only releases the connection and the embedded source.
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// migrateURL turns a postgres:// or postgresql:// URL into the pgx5:// URL that selects
// golang-migrate's pgx v5 driver, and adds connect_timeout and lock_timeout when they are missing.
func migrateURL(databaseURL string) (string, error) {
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
	if !q.Has("lock_timeout") {
		q.Set("lock_timeout", "30s")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
