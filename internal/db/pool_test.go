package db_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/db"
)

func TestOpenKeepsThePasswordOutOfErrors(t *testing.T) {
	tests := map[string]string{
		"postgres://solpay:pw-SECRET@db:notaport/solpay":                     "database URL is not a valid URL",
		"postgres://solpay:pw@SECRET@db:notaport/solpay":                     "database URL is not a valid URL",
		"postgres://solpay:pw-SECRET@127.0.0.1:1/solpay?connect_timeout=5":   "connect to the database",
		"host=127.0.0.1 port=1 user=solpay password=pw-SECRET dbname=solpay": "connect to the database",
	}
	for url, want := range tests {
		pool, err := db.Open(t.Context(), url)
		require.ErrorContains(t, err, want, url)
		assert.NotContains(t, err.Error(), "SECRET", url)
		assert.Nil(t, pool)
	}
}
