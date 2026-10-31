package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{
			"postgres://solpay:pw@db:5432/solpay?sslmode=disable",
			"pgx5://solpay:pw@db:5432/solpay?connect_timeout=10&lock_timeout=30s&sslmode=disable",
		},
		{
			"POSTGRESQL://solpay@localhost/solpay",
			"pgx5://solpay@localhost/solpay?connect_timeout=10&lock_timeout=30s",
		},
		{
			"postgres://solpay@localhost/solpay?connect_timeout=2&lock_timeout=5s",
			"pgx5://solpay@localhost/solpay?connect_timeout=2&lock_timeout=5s",
		},
	}
	for _, tt := range tests {
		got, err := migrateURL(tt.in)
		require.NoError(t, err, tt.in)
		assert.Equal(t, tt.want, got, tt.in)
	}
}

func TestMigrateURLRejectsOtherSchemes(t *testing.T) {
	_, err := migrateURL("mysql://solpay@localhost/solpay")
	require.EqualError(t, err, `database URL scheme "mysql": want postgres or postgresql`)
}

func TestMigrateErrorOmitsPassword(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		// Nothing listens on port 1, so the connection fails before any migration runs.
		err := Migrate("postgres://solpay:db-password@127.0.0.1:1/solpay?sslmode=disable&connect_timeout=2")
		require.ErrorContains(t, err, "failed to connect")
		assert.NotContains(t, err.Error(), "db-password")
	})
	t.Run("malformed URL", func(t *testing.T) {
		err := Migrate("postgres://solpay:db-password%zz@127.0.0.1:1/solpay")
		require.EqualError(t, err, "database URL is not a valid URL")
	})
}
