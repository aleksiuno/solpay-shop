//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/db"
	"github.com/aleksiuno/solpay-shop/migrations"
)

// TestMigrateUpDownUp checks that the down migrations undo the up migrations completely. It
// leaves the database fully migrated, but other tests must not run in parallel with it.
func TestMigrateUpDownUp(t *testing.T) {
	all := []string{"orders", "payment_events", "payments", "products", "schema_migrations"}

	// TestMain has applied every migration, so a second run finds nothing to do.
	require.NoError(t, db.Migrate(dsn))
	assert.Equal(t, all, tables(t))

	src, err := iofs.New(migrations.FS, ".")
	require.NoError(t, err)
	m, err := migrate.NewWithSourceInstance("iofs", src, "pgx5"+strings.TrimPrefix(dsn, "postgres"))
	require.NoError(t, err)
	require.NoError(t, m.Down())
	srcErr, dbErr := m.Close()
	require.NoError(t, srcErr)
	require.NoError(t, dbErr)
	assert.Equal(t, []string{"schema_migrations"}, tables(t))

	require.NoError(t, db.Migrate(dsn))
	assert.Equal(t, all, tables(t))

	var version int
	var dirty bool
	conn := connect(t)
	require.NoError(t, conn.QueryRow(t.Context(), "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty))
	assert.Equal(t, 1, version)
	assert.False(t, dirty)
}

// TestMigrateGivesUpOnAHeldLock holds golang-migrate's advisory lock in another session, as a
// server still migrating would. Each of the two waits for it must end with Postgres's lock
// timeout, so that no pg_advisory_lock call is left running for Close to wait on.
func TestMigrateGivesUpOnAHeldLock(t *testing.T) {
	const wait = time.Second

	t.Run("while opening", func(t *testing.T) {
		holdMigrateLock(t)
		start := time.Now()
		_, err := db.NewMigrator(dsn, wait)
		requireLockTimeout(t, err, time.Since(start), wait)
	})

	t.Run("before applying", func(t *testing.T) {
		m, err := db.NewMigrator(dsn, wait)
		require.NoError(t, err)
		holdMigrateLock(t)
		start := time.Now()
		err = m.Up()
		took := time.Since(start)
		// Close comes before the checks, so that a failed one leaves no lock call running into
		// the next test.
		start = time.Now()
		srcErr, dbErr := m.Close()
		closeTook := time.Since(start)

		requireLockTimeout(t, err, took, wait)
		assert.Less(t, closeTook, wait/2, "Close waited for the lock")
		require.NoError(t, srcErr)
		require.NoError(t, dbErr)
	})

	t.Run("golang-migrate's timeout outlasts lock_timeout", func(t *testing.T) {
		// Past golang-migrate's default, which would otherwise end the second wait first.
		wait := 2 * migrate.DefaultLockTimeout
		m, err := db.NewMigrator(dsn, wait)
		require.NoError(t, err)
		t.Cleanup(func() { m.Close() })
		assert.Greater(t, m.LockTimeout, wait)
	})
}

// holdMigrateLock takes golang-migrate's advisory lock in a session of its own until the test
// ends. It goes through golang-migrate's driver, so the lock is the one Migrate asks for.
func holdMigrateLock(t *testing.T) {
	t.Helper()
	drv, err := database.Open("pgx5" + strings.TrimPrefix(dsn, "postgres"))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, drv.Close()) })
	require.NoError(t, drv.Lock())
	// Postgres frees a closed session's locks only once its backend exits, so the lock is
	// released here, before the next test asks for it.
	t.Cleanup(func() { assert.NoError(t, drv.Unlock()) })
}

// requireLockTimeout checks that err is Postgres's lock timeout, which came once wait had passed
// and not much later.
func requireLockTimeout(t *testing.T, err error, took, wait time.Duration) {
	t.Helper()
	// golang-migrate's errors do not unwrap to pgconn's, so the code is matched in the text.
	require.ErrorContains(t, err, "SQLSTATE 55P03")
	assert.GreaterOrEqual(t, took, wait)
	assert.Less(t, took, wait+2*time.Second)
}

func connect(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

// tables lists the tables in the public schema, sorted by name.
func tables(t *testing.T) []string {
	t.Helper()
	rows, err := connect(t).Query(t.Context(), "SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename")
	require.NoError(t, err)
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return names
}
