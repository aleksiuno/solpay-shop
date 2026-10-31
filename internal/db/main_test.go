//go:build integration

package db_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/aleksiuno/solpay-shop/internal/db"
)

// dsn is the URL of the package's Postgres container, which TestMain has already migrated.
var dsn string

// TestMain starts one Postgres container for the package. With the integration tag set and no
// Docker daemon, the tests fail instead of skipping, so CI cannot pass without running them.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("solpay"),
		postgres.WithUsername("solpay"),
		postgres.WithPassword(rand.Text()),
		postgres.BasicWaitStrategies(),
	)
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			fmt.Fprintln(os.Stderr, "terminate postgres:", err)
		}
	}()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start postgres:", err)
		return 1
	}
	if dsn, err = ctr.ConnectionString(ctx, "sslmode=disable"); err != nil {
		fmt.Fprintln(os.Stderr, "postgres connection string:", err)
		return 1
	}
	if err := db.Migrate(dsn); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		return 1
	}
	return m.Run()
}
