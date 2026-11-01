package order_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/order"
)

func TestRemainingLamports(t *testing.T) {
	tests := []struct {
		received, want int64
	}{
		{0, 1000},
		{1, 999},
		{400, 600},
		{999, 1},
		{1000, 0},
		{1500, 0},
	}
	for _, tt := range tests {
		o := order.Order{AmountLamports: 1000, ReceivedLamports: tt.received}
		assert.Equal(t, tt.want, o.RemainingLamports(), "received %d", tt.received)
	}
}

func TestOnTime(t *testing.T) {
	deadline := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	o := order.Order{ExpiresAt: deadline}
	assert.True(t, o.OnTime(deadline.Add(-time.Hour)))
	assert.True(t, o.OnTime(deadline.Add(-time.Nanosecond)))
	assert.True(t, o.OnTime(deadline), "a payment at the deadline counts")
	assert.True(t, o.OnTime(deadline.In(time.FixedZone("UTC+2", 2*60*60))), "the same instant in another zone")
	assert.False(t, o.OnTime(deadline.Add(time.Nanosecond)))
	assert.False(t, o.OnTime(deadline.Add(time.Second)))
}

func TestCredit(t *testing.T) {
	tests := []struct {
		name         string
		status       order.Status
		received     int64
		credit       int64
		wantStatus   order.Status
		wantReceived int64
	}{
		{"exact payment", order.Pending, 0, 1000, order.Paid, 1000},
		{"overpayment", order.Pending, 0, 1500, order.Paid, 1500},
		{"short payment", order.Pending, 0, 999, order.Underpaid, 999},
		{"one lamport", order.Pending, 0, 1, order.Underpaid, 1},
		{"top-up still short", order.Underpaid, 400, 500, order.Underpaid, 900},
		{"top-up to the amount", order.Underpaid, 400, 600, order.Paid, 1000},
		{"top-up past the amount", order.Underpaid, 400, 700, order.Paid, 1100},
		{"last lamport", order.Underpaid, 999, 1, order.Paid, 1000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := order.Order{Status: tt.status, AmountLamports: 1000, ReceivedLamports: tt.received}
			next, received, err := o.Credit(tt.credit)
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, next)
			assert.Equal(t, tt.wantReceived, received)
			assert.Equal(t, tt.status, o.Status, "Credit changes nothing itself")
			assert.Equal(t, tt.received, o.ReceivedLamports, "Credit changes nothing itself")
		})
	}
}

func TestCreditRefusesOtherStatuses(t *testing.T) {
	for _, s := range []order.Status{order.Paid, order.Fulfilled, order.Expired, order.Refunded, "", "cancelled"} {
		o := order.Order{Status: s, AmountLamports: 1000}
		next, received, err := o.Credit(1000)
		require.ErrorIs(t, err, order.ErrInvalidTransition, "status %q", s)
		assert.Empty(t, next)
		assert.Zero(t, received)
	}
}

func TestCreditRefusesBadAmounts(t *testing.T) {
	o := order.Order{Status: order.Pending, AmountLamports: 1000}
	for _, lamports := range []int64{0, -1, math.MinInt64} {
		_, _, err := o.Credit(lamports)
		require.ErrorContains(t, err, "cannot credit", "%d lamports", lamports)
		require.NotErrorIs(t, err, order.ErrInvalidTransition)
	}

	o = order.Order{Status: order.Underpaid, AmountLamports: math.MaxInt64, ReceivedLamports: math.MaxInt64 - 10}
	_, _, err := o.Credit(11)
	require.ErrorContains(t, err, "overflows int64")
	require.NotErrorIs(t, err, order.ErrInvalidTransition)

	next, received, err := o.Credit(10)
	require.NoError(t, err, "a total of exactly MaxInt64 fits")
	assert.Equal(t, order.Paid, next)
	assert.Equal(t, int64(math.MaxInt64), received)
}
