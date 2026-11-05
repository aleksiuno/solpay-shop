//go:build integration

package db_test

import (
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/db"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
	"github.com/aleksiuno/solpay-shop/internal/solana/solanatest"
)

// Two ticks of the watcher against Postgres, with a fake node: the first credits a part payment
// to one order and expires another, and the second, which finds the same transaction again,
// changes nothing but the next poll times. The clock starts at watchTime, so that the watch set
// holds only this test's orders.
func TestWatcherTicksTwiceAgainstPostgres(t *testing.T) {
	payments, _ := newPayments()
	chain := solanatest.NewFake()
	cfg := payment.DefaultConfig(5 * time.Second)
	now := watchTime
	rec := audit.NewRecorder(db.NewEvents(pool), slog.New(slog.DiscardHandler))
	w := payment.NewWatcher(chain, payments, rec, solanatest.Merchant, cfg, func() time.Time { return now })

	partPaid := watchedOrder(t, order.Pending, now.Add(15*time.Minute), new(now)) // 0.004 SOL
	unpaid := watchedOrder(t, order.Pending, now.Add(-cfg.ExpiryGrace-time.Second), new(now))
	// The recorded devpay payment of 0.001 SOL, made now, for partPaid.
	tx := solanatest.Devpay.Tx(t)
	tx.Signature, tx.BlockTime = newSignature(), new(now)
	tx.Keys[slices.Index(tx.Keys, solanatest.Devpay.Reference)] = partPaid.Reference
	chain.Add(partPaid.Reference, tx)

	for range 2 {
		n, err := w.RunOnce(t.Context())
		require.NoError(t, err)
		require.Equal(t, 2, n)

		got := getOrder(t, partPaid.ID)
		assert.Equal(t, order.Underpaid, got.Status)
		assert.Equal(t, int64(1_000_000), got.ReceivedLamports)
		assert.Equal(t, []audit.Kind{audit.PaymentUnderpaid, audit.PaymentSeen}, eventKinds(t, partPaid.ID))
		assert.WithinDuration(t, now.Add(cfg.PollInterval), *nextPoll(t, partPaid.ID), 0, "before its deadline")
		assert.Equal(t, order.Expired, getOrder(t, unpaid.ID).Status)
		assert.Equal(t, []audit.Kind{audit.OrderExpired}, eventKinds(t, unpaid.ID))
		assert.WithinDuration(t, now.Add(cfg.LateCadence), *nextPoll(t, unpaid.ID), 0, "in its late window")
		now = now.Add(cfg.LateCadence)
	}
	assert.Equal(t, 1, chain.Calls(solanatest.GetTransaction))
	row, ok := ledger(t, tx.Signature)
	require.True(t, ok)
	assert.Equal(t, ledgerRow{
		OrderID: partPaid.ID, Outcome: "credited", Lamports: 1_000_000, Slot: solanatest.Devpay.Slot, BlockTime: row.BlockTime,
	}, row)
	assert.WithinDuration(t, watchTime, *row.BlockTime, 0)
}
