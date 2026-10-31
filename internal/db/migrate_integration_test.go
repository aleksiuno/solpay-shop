//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
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
