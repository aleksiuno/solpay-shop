package config_test

import (
	"crypto/sha256"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mr-tron/base58"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/config"
)

// testAddress returns a valid base58 address derived from label, so no real key is needed.
func testAddress(label string) string {
	sum := sha256.Sum256([]byte(label))
	return base58.Encode(sum[:])
}

// validEnv returns an environment with every required variable valid and every optional one unset.
func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":     "postgres://solpay:pw@localhost:5432/solpay?sslmode=disable",
		"MERCHANT_ADDRESS": testAddress("merchant"),
		"ADMIN_USER":       "admin",
		"ADMIN_PASS":       "twelve-chars",
		"DOWNLOAD_SECRET":  strings.Repeat("s", 32),
	}
}

func getenv(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

// failedKeys returns the sorted keys of the settings that Load rejected.
func failedKeys(t *testing.T, err error) []string {
	t.Helper()
	joined, ok := err.(interface{ Unwrap() []error })
	require.True(t, ok, "Load should join its errors: %v", err)
	var keys []string
	for _, e := range joined.Unwrap() {
		fe, ok := errors.AsType[*config.FieldError](e)
		require.True(t, ok, "not a FieldError: %v", e)
		keys = append(keys, fe.Key)
	}
	slices.Sort(keys)
	return keys
}

func TestLoadDefaults(t *testing.T) {
	env := validEnv()
	c, err := config.Load(getenv(env))
	require.NoError(t, err)

	assert.Equal(t, config.Config{
		Addr:            ":8080",
		DatabaseURL:     env["DATABASE_URL"],
		SolanaRPCURL:    "https://api.devnet.solana.com",
		MerchantAddress: env["MERCHANT_ADDRESS"],
		OrderTTL:        15 * time.Minute,
		PollInterval:    5 * time.Second,
		AdminUser:       env["ADMIN_USER"],
		AdminPass:       env["ADMIN_PASS"],
		DownloadSecret:  []byte(env["DOWNLOAD_SECRET"]),
		FilesDir:        "./testdata/files",
	}, c)
}

func TestLoadOverrides(t *testing.T) {
	env := validEnv()
	env["APP_ADDR"] = "127.0.0.1:9090"
	env["SOLANA_RPC_URL"] = "http://localhost:8899"
	env["ORDER_TTL"] = "1h"
	env["POLL_INTERVAL"] = "1s"
	env["LOG_FILE"] = "./data/app.log"
	env["FILES_DIR"] = "/srv/files"

	c, err := config.Load(getenv(env))
	require.NoError(t, err)

	assert.Equal(t, config.Config{
		Addr:            "127.0.0.1:9090",
		DatabaseURL:     env["DATABASE_URL"],
		SolanaRPCURL:    "http://localhost:8899",
		MerchantAddress: env["MERCHANT_ADDRESS"],
		OrderTTL:        time.Hour,
		PollInterval:    time.Second,
		AdminUser:       env["ADMIN_USER"],
		AdminPass:       env["ADMIN_PASS"],
		LogFile:         "./data/app.log",
		DownloadSecret:  []byte(env["DOWNLOAD_SECRET"]),
		FilesDir:        "/srv/files",
	}, c)
}

func TestLoadEmptyEnvironmentNamesEveryRequiredVariable(t *testing.T) {
	_, err := config.Load(getenv(nil))
	require.Error(t, err)
	assert.Equal(t,
		[]string{"ADMIN_PASS", "ADMIN_USER", "DATABASE_URL", "DOWNLOAD_SECRET", "MERCHANT_ADDRESS"},
		failedKeys(t, err))
}

func TestLoadAcceptsBoundaryValues(t *testing.T) {
	tests := []struct{ key, value string }{
		{"APP_ADDR", ":0"},
		{"APP_ADDR", "[::1]:65535"},
		{"DATABASE_URL", "postgresql://solpay@db/solpay"},
		{"SOLANA_RPC_URL", "http://127.0.0.1:8899"},
		{"SOLANA_RPC_URL", "http://[::1]:8899"},
		{"ORDER_TTL", "1m"},
		{"ORDER_TTL", "24h"},
		{"POLL_INTERVAL", "1s"},
		{"POLL_INTERVAL", "1m"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			env := validEnv()
			env[tt.key] = tt.value
			_, err := config.Load(getenv(env))
			assert.NoError(t, err)
		})
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"address without a port", "APP_ADDR", "localhost"},
		{"address with an empty port", "APP_ADDR", "localhost:"},
		{"address with a named port", "APP_ADDR", ":abc"},
		{"address with a port above 65535", "APP_ADDR", ":99999"},
		{"database URL with another scheme", "DATABASE_URL", "mysql://solpay@localhost/solpay"},
		{"database URL without a host", "DATABASE_URL", "postgres:///solpay"},
		{"database URL with a port but no host", "DATABASE_URL", "postgres://:5432/solpay"},
		{"RPC URL with another scheme", "SOLANA_RPC_URL", "ftp://api.devnet.solana.com"},
		{"RPC URL with a port but no host", "SOLANA_RPC_URL", "https://:443"},
		{"RPC URL over plain http to a remote host", "SOLANA_RPC_URL", "http://api.devnet.solana.com"},
		{"merchant address with non-base58 characters", "MERCHANT_ADDRESS", "your-devnet-wallet-address"},
		{"merchant address that is not 32 bytes", "MERCHANT_ADDRESS", base58.Encode([]byte("too short"))},
		{"order TTL that is not a duration", "ORDER_TTL", "15"},
		{"order TTL below the minimum", "ORDER_TTL", "30s"},
		{"order TTL above the maximum", "ORDER_TTL", "25h"},
		{"poll interval that is not a duration", "POLL_INTERVAL", "5"},
		{"poll interval below the minimum", "POLL_INTERVAL", "500ms"},
		{"poll interval above the maximum", "POLL_INTERVAL", "2m"},
		{"admin user with a colon", "ADMIN_USER", "ad:min"},
		{"admin password under 12 characters", "ADMIN_PASS", "elevenchars"},
		{"admin password of 12 bytes but 6 characters", "ADMIN_PASS", "ąčęėįš"},
		{"download secret under 32 bytes", "DOWNLOAD_SECRET", strings.Repeat("s", 31)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			env[tt.key] = tt.value
			_, err := config.Load(getenv(env))
			require.Error(t, err)
			assert.Equal(t, []string{tt.key}, failedKeys(t, err))
		})
	}
}

func TestLoadDurationErrorsUseShortUnits(t *testing.T) {
	env := validEnv()
	env["ORDER_TTL"] = "59s"
	_, err := config.Load(getenv(env))
	require.EqualError(t, err, "ORDER_TTL: must be between 1m and 24h")
}

func TestLoadErrorsNeverContainValues(t *testing.T) {
	env := validEnv()
	env["DATABASE_URL"] = "mysql://solpay:db-password@localhost/solpay"
	env["SOLANA_RPC_URL"] = "ftp://rpc.example.com/?api-key=rpc-api-key"
	env["ADMIN_PASS"] = "short-pass"
	env["DOWNLOAD_SECRET"] = "short-secret"

	_, err := config.Load(getenv(env))
	require.Error(t, err)
	for _, value := range []string{"db-password", "rpc-api-key", "short-pass", "short-secret"} {
		assert.NotContains(t, err.Error(), value)
	}
}

// TestEnvExamplePlaceholdersAreRejected keeps .env.example honest: an unfilled copy must not start
// the service with a known password or secret, or with a placeholder merchant address, and the
// file must list every variable Load reads.
func TestEnvExamplePlaceholdersAreRejected(t *testing.T) {
	data, err := os.ReadFile("../../.env.example")
	require.NoError(t, err)

	env := map[string]string{}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		require.True(t, ok, "not a KEY=VALUE line: %q", line)
		env[key] = os.Expand(value, func(k string) string { return env[k] })
	}

	var read []string
	_, err = config.Load(func(key string) string {
		read = append(read, key)
		return env[key]
	})
	require.Error(t, err)
	assert.Equal(t, []string{"ADMIN_PASS", "DOWNLOAD_SECRET", "MERCHANT_ADDRESS"}, failedKeys(t, err))
	for _, key := range read {
		assert.Contains(t, env, key, ".env.example does not list %s", key)
	}
}
