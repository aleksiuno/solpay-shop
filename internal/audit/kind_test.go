package audit_test

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/migrations"
)

// kinds lists the 16 kinds with the value the database stores and the level each must have, written
// out here rather than taken from the code under test.
var kinds = []struct {
	kind  audit.Kind
	value string
	level audit.Level
}{
	{audit.OrderCreated, "order.created", audit.Info},
	{audit.OrderExpired, "order.expired", audit.Info},
	{audit.OrderRefunded, "order.refunded", audit.Info},
	{audit.PaymentSeen, "payment.seen", audit.Info},
	{audit.PaymentVerified, "payment.verified", audit.Info},
	{audit.PaymentDuplicateIgnored, "payment.duplicate_ignored", audit.Info},
	{audit.PaymentUnderpaid, "payment.underpaid", audit.Warn},
	{audit.PaymentOverpaid, "payment.overpaid", audit.Warn},
	{audit.PaymentLate, "payment.late", audit.Warn},
	{audit.PaymentInvalidTx, "payment.invalid_tx", audit.Warn},
	{audit.DownloadIssued, "download.issued", audit.Info},
	{audit.RPCError, "rpc.error", audit.Error},
	{audit.RPCTimeout, "rpc.timeout", audit.Warn},
	{audit.WatcherStarted, "watcher.started", audit.Info},
	{audit.WatcherStopped, "watcher.stopped", audit.Info},
	{audit.WatcherError, "watcher.error", audit.Error},
}

func TestKindLevels(t *testing.T) {
	require.Len(t, kinds, 16)
	for _, k := range kinds {
		assert.Equal(t, k.value, string(k.kind))
		assert.Equal(t, k.level, k.kind.Level(), "%s", k.kind)
	}
}

func TestUnknownKindIsAnError(t *testing.T) {
	for _, k := range []audit.Kind{"", "order", "ORDER.CREATED", "payment.seen ", "payment.refunded"} {
		assert.Equal(t, audit.Error, k.Level(), "%q", k)
	}
}

// TestLevelsMatchSchema checks the level constants against the level CHECK in the migrations.
func TestLevelsMatchSchema(t *testing.T) {
	files, err := fs.Glob(migrations.FS, "*.up.sql")
	require.NoError(t, err)
	slices.Sort(files)
	check := regexp.MustCompile(`CHECK \(level IN \(([^)]*)\)\)`)
	var values string
	for _, f := range files {
		b, err := fs.ReadFile(migrations.FS, f)
		require.NoError(t, err)
		for _, m := range check.FindAllSubmatch(b, -1) {
			values = string(m[1])
		}
	}
	require.NotEmpty(t, values, "no level CHECK in the migrations")
	var schema []audit.Level
	for v := range strings.SplitSeq(values, ",") {
		schema = append(schema, audit.Level(strings.Trim(strings.TrimSpace(v), "'")))
	}
	assert.ElementsMatch(t, []audit.Level{audit.Info, audit.Warn, audit.Error}, schema)
}
