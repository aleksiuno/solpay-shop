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

func TestPayable(t *testing.T) {
	deadline := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	// takes lists the statuses that take payments, written out here.
	takes := map[order.Status]bool{order.Pending: true, order.Underpaid: true}
	for _, s := range order.Statuses() {
		o := order.Order{Status: s, ExpiresAt: deadline}
		assert.Equal(t, takes[s], o.Payable(deadline.Add(-time.Minute)), "%s before the deadline", s)
		assert.Equal(t, takes[s], o.Payable(deadline), "%s at the deadline", s)
		assert.False(t, o.Payable(deadline.Add(time.Nanosecond)), "%s after the deadline", s)
	}
}

// An order may be refunded once it has received something: on time or, for an expired one, late.
func TestCheckRefund(t *testing.T) {
	for _, s := range []order.Status{order.Underpaid, order.Paid, order.Fulfilled} {
		require.NoError(t, order.Order{Status: s}.CheckRefund(), s)
	}
	for _, o := range []order.Order{
		{Status: order.Pending}, {Status: order.Pending, HasLatePayment: true},
		{Status: order.Refunded}, {Status: order.Refunded, HasLatePayment: true},
		{Status: "cancelled"},
	} {
		err := o.CheckRefund()
		require.ErrorIs(t, err, order.ErrInvalidTransition, "%s, late payment %t", o.Status, o.HasLatePayment)
		require.NotErrorIs(t, err, order.ErrNoLatePayment, "%s, late payment %t", o.Status, o.HasLatePayment)
	}

	expired := order.Order{Status: order.Expired}
	require.ErrorIs(t, expired.CheckRefund(), order.ErrNoLatePayment)
	expired.HasLatePayment = true
	require.NoError(t, expired.CheckRefund(), "a late payment makes an expired order refundable")
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
		// A paid or fulfilled order keeps its status: the payment is an overpayment.
		{"to a paid order", order.Paid, 1000, 300, order.Paid, 1300},
		{"to an overpaid order", order.Paid, 1500, 1, order.Paid, 1501},
		{"to a fulfilled order", order.Fulfilled, 1000, 300, order.Fulfilled, 1300},
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
	for _, s := range []order.Status{order.Expired, order.Refunded, "", "cancelled"} {
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

	for _, o := range []order.Order{
		{Status: order.Underpaid, AmountLamports: math.MaxInt64, ReceivedLamports: math.MaxInt64 - 10},
		{Status: order.Paid, AmountLamports: 1000, ReceivedLamports: math.MaxInt64 - 10},
	} {
		_, _, err := o.Credit(11)
		require.ErrorContains(t, err, "overflows int64", o.Status)
		require.NotErrorIs(t, err, order.ErrInvalidTransition, o.Status)

		next, received, err := o.Credit(10)
		require.NoError(t, err, "%s: a total of exactly MaxInt64 fits", o.Status)
		assert.Equal(t, order.Paid, next, o.Status)
		assert.Equal(t, int64(math.MaxInt64), received, o.Status)
	}
}
