//go:build integration

package db_test

import (
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
)

// The ledger lists the order's rows by slot, then by when each was seen, with their outcomes,
// reasons and block times, and nothing of other orders.
func TestPaymentsLedger(t *testing.T) {
	payments, _ := newPayments()
	o := storedOrder(t, order.Paid)
	other := storedOrder(t, order.Paid)
	now, blockTime := testNow(), testNow().Add(-time.Minute)
	insert := func(sig string, id uuid.UUID, outcome string, lamports, slot int64, at *time.Time, reason *string, seen time.Time) {
		t.Helper()
		_, err := pool.Exec(t.Context(), `INSERT INTO payments (signature, order_id, outcome, lamports, slot, block_time, reason, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, sig, id, outcome, lamports, slot, at, reason, seen)
		require.NoError(t, err)
	}
	// Two rows of one slot, the first seen with the signature that sorts after the other's.
	first, second := newSignature(), newSignature()
	if first < second {
		first, second = second, first
	}
	late := newSignature()
	failed := string(payment.TxFailed)
	insert(late, o.ID, "late", 2_000, 9, &blockTime, nil, now)
	insert(second, o.ID, "rejected", 0, 3, nil, &failed, now.Add(-time.Second))
	insert(first, o.ID, "credited", 10_000_000, 3, &blockTime, nil, now.Add(-2*time.Second))
	insert(newSignature(), other.ID, "credited", 1_000, 1, nil, nil, now)

	got, err := payments.Ledger(t.Context(), o.ID)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{first, second, late}, []string{got[0].Signature, got[1].Signature, got[2].Signature},
		"by slot, then by when each was seen")
	assert.Equal(t, payment.Credited, got[0].Outcome)
	assert.Equal(t, int64(10_000_000), got[0].Lamports)
	assert.Equal(t, int64(3), got[0].Slot)
	assert.Equal(t, o.ID, got[0].OrderID)
	require.NotNil(t, got[0].BlockTime)
	assert.WithinDuration(t, blockTime, *got[0].BlockTime, 0)
	assert.Equal(t, payment.Rejected, got[1].Outcome)
	assert.Equal(t, payment.TxFailed, got[1].Reason)
	assert.Nil(t, got[1].BlockTime)
	assert.Equal(t, payment.Late, got[2].Outcome)
	assert.Empty(t, got[2].Reason)

	none, err := payments.Ledger(t.Context(), uuid.NewV4())
	require.NoError(t, err)
	assert.Empty(t, none)
}
