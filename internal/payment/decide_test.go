package payment_test

import (
	"fmt"
	"math"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
)

var (
	deadline = time.Date(2026, 11, 4, 12, 15, 0, 0, time.UTC)
	before   = deadline.Add(-5 * time.Minute) // a block time on time
	after    = deadline.Add(time.Second)      // a block time past the deadline
	// open is an order of 0.01 SOL that has received nothing.
	open = order.Order{
		ID:             uuid.MustParse("5d0c7a3e-8b1f-4f2a-9c6d-2e4b8a1f3c5d"),
		AmountLamports: 10_000_000,
		Reference:      reference,
		Status:         order.Pending,
		ExpiresAt:      deadline,
	}
)

const sig = "sig1"

// candidate is a valid transaction paying lamports at the block time at, or with no block time
// if at is nil.
func candidate(lamports int64, at *time.Time) payment.Candidate {
	return payment.Candidate{Signature: sig, Slot: 42, BlockTime: at, Verdict: payment.Verdict{Lamports: lamports}}
}

// with returns open after change.
func with(change func(o *order.Order)) order.Order {
	o := open
	change(&o)
	return o
}

// ev is an event about open and sig.
func ev(k audit.Kind, msg string, data map[string]any) audit.Event {
	return audit.Event{OrderID: &open.ID, Kind: k, Message: msg, Signature: sig, Data: data}
}

func seen(at *time.Time) audit.Event {
	var bt any
	if at != nil {
		bt = at.Format(time.RFC3339Nano)
	}
	return ev(audit.PaymentSeen, "payment seen", map[string]any{"slot": int64(42), "block_time": bt})
}

func row(outcome payment.Outcome, lamports int64, at *time.Time) payment.Payment {
	return payment.Payment{Signature: sig, OrderID: open.ID, Outcome: outcome, Lamports: lamports, Slot: 42, BlockTime: at}
}

func TestDecideCredits(t *testing.T) {
	now := deadline.Add(-time.Minute)
	atDeadline := deadline
	tests := []struct {
		name string
		o    order.Order
		c    payment.Candidate
		want payment.Decision
	}{
		{"the exact amount", open, candidate(10_000_000, &before), payment.Decision{
			Payment: row(payment.Credited, 10_000_000, &before), Status: order.Paid, Received: 10_000_000, PaidAt: &before,
			Events: []audit.Event{seen(&before), ev(audit.PaymentVerified, "order paid", map[string]any{
				"lamports": int64(10_000_000), "received_lamports": int64(10_000_000), "amount_lamports": int64(10_000_000),
			})},
		}},
		{"more than the amount", open, candidate(15_000_000, &before), payment.Decision{
			Payment: row(payment.Credited, 15_000_000, &before), Status: order.Paid, Received: 15_000_000, PaidAt: &before,
			Events: []audit.Event{seen(&before), ev(audit.PaymentVerified, "order paid", map[string]any{
				"lamports": int64(15_000_000), "received_lamports": int64(15_000_000), "amount_lamports": int64(10_000_000),
			}), ev(audit.PaymentOverpaid, "order received more than its amount", map[string]any{"surplus_lamports": int64(5_000_000)})},
		}},
		{"less than the amount", open, candidate(4_000_000, &before), payment.Decision{
			Payment: row(payment.Credited, 4_000_000, &before), Status: order.Underpaid, Received: 4_000_000,
			Events: []audit.Event{seen(&before), ev(audit.PaymentUnderpaid, "payment leaves the order short", map[string]any{
				"lamports": int64(4_000_000), "received_lamports": int64(4_000_000), "remaining_lamports": int64(6_000_000),
			})},
		}},
		{"a top-up that completes the amount", with(func(o *order.Order) { o.Status, o.ReceivedLamports = order.Underpaid, 4_000_000 }),
			candidate(6_000_000, &before), payment.Decision{
				Payment: row(payment.Credited, 6_000_000, &before), Status: order.Paid, Received: 10_000_000, PaidAt: &before,
				Events: []audit.Event{seen(&before), ev(audit.PaymentVerified, "order paid", map[string]any{
					"lamports": int64(6_000_000), "received_lamports": int64(10_000_000), "amount_lamports": int64(10_000_000),
				})},
			}},
		{"a top-up past the amount", with(func(o *order.Order) { o.Status, o.ReceivedLamports = order.Underpaid, 4_000_000 }),
			candidate(7_000_000, &before), payment.Decision{
				Payment: row(payment.Credited, 7_000_000, &before), Status: order.Paid, Received: 11_000_000, PaidAt: &before,
				Events: []audit.Event{seen(&before), ev(audit.PaymentVerified, "order paid", map[string]any{
					"lamports": int64(7_000_000), "received_lamports": int64(11_000_000), "amount_lamports": int64(10_000_000),
				}), ev(audit.PaymentOverpaid, "order received more than its amount",
					map[string]any{"surplus_lamports": int64(1_000_000)})},
			}},
		{"a top-up still short", with(func(o *order.Order) { o.Status, o.ReceivedLamports = order.Underpaid, 4_000_000 }),
			candidate(1_000_000, &before), payment.Decision{
				Payment: row(payment.Credited, 1_000_000, &before), Status: order.Underpaid, Received: 5_000_000,
				Events: []audit.Event{seen(&before), ev(audit.PaymentUnderpaid, "payment leaves the order short", map[string]any{
					"lamports": int64(1_000_000), "received_lamports": int64(5_000_000), "remaining_lamports": int64(5_000_000),
				})},
			}},
		{"a block time at the deadline", open, candidate(10_000_000, &atDeadline), payment.Decision{
			Payment: row(payment.Credited, 10_000_000, &atDeadline), Status: order.Paid, Received: 10_000_000, PaidAt: &atDeadline,
			Events: []audit.Event{seen(&atDeadline), ev(audit.PaymentVerified, "order paid", map[string]any{
				"lamports": int64(10_000_000), "received_lamports": int64(10_000_000), "amount_lamports": int64(10_000_000),
			})},
		}},
		{"no block time, before the deadline", open, candidate(10_000_000, nil), payment.Decision{
			Payment: row(payment.Credited, 10_000_000, nil), Status: order.Paid, Received: 10_000_000, PaidAt: &now,
			Events: []audit.Event{seen(nil), ev(audit.PaymentVerified, "order paid", map[string]any{
				"lamports": int64(10_000_000), "received_lamports": int64(10_000_000), "amount_lamports": int64(10_000_000),
			})},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := payment.Decide(tt.o, tt.c, now)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A payment made on time counts even when it is first seen after the deadline, as it can be while
// the RPC node is slow to index it.
func TestDecideCreditsAPaymentOnTimeSeenLate(t *testing.T) {
	got, err := payment.Decide(open, candidate(10_000_000, &before), deadline.Add(90*time.Second))
	require.NoError(t, err)
	assert.Equal(t, payment.Credited, got.Payment.Outcome)
	assert.Equal(t, order.Paid, got.Status)
	assert.Equal(t, &before, got.PaidAt, "paid when the payment was made, not when it was seen")
}

// A paid order can still receive a payment, sent in the same poll as the one that paid it, or
// twice by mistake. It counts, as an overpayment, and the event gives the order's whole surplus.
func TestDecideCreditsAPaymentToAPaidOrder(t *testing.T) {
	paidAt := before.Add(-time.Minute)
	tests := []struct {
		status            order.Status
		received, surplus int64
	}{
		{order.Paid, 10_000_000, 3_000_000},
		{order.Fulfilled, 10_000_000, 3_000_000},
		{order.Paid, 15_000_000, 8_000_000}, // already overpaid
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s with %d", tt.status, tt.received), func(t *testing.T) {
			o := with(func(o *order.Order) { o.Status, o.ReceivedLamports, o.PaidAt = tt.status, tt.received, &paidAt })
			got, err := payment.Decide(o, candidate(3_000_000, &before), before)
			require.NoError(t, err)
			assert.Equal(t, payment.Decision{
				Payment: row(payment.Credited, 3_000_000, &before), Status: tt.status, Received: tt.received + 3_000_000, PaidAt: &paidAt,
				Events: []audit.Event{seen(&before), ev(audit.PaymentOverpaid, "order received more than its amount",
					map[string]any{"surplus_lamports": tt.surplus})},
			}, got)
		})
	}
}

func TestDecideRecordsALatePayment(t *testing.T) {
	paidAt := before
	tests := []struct {
		name   string
		o      order.Order
		at     *time.Time
		now    time.Time
		onTime bool
	}{
		{"pending, after the deadline", open, &after, after, false},
		// The store reads times in the local zone; the event gives them in UTC.
		{"pending, with its deadline in another zone", with(func(o *order.Order) {
			o.ExpiresAt = deadline.In(time.FixedZone("EET", 2*60*60))
		}), &after, after, false},
		{"pending, no block time, seen after the deadline", open, nil, after, false},
		{"underpaid, after the deadline", with(func(o *order.Order) { o.Status, o.ReceivedLamports = order.Underpaid, 4_000_000 }),
			&after, after, false},
		{"paid, after the deadline", with(func(o *order.Order) { o.Status, o.ReceivedLamports, o.PaidAt = order.Paid, 10_000_000, &paidAt }),
			&after, after, false},
		{"expired, after the deadline", with(func(o *order.Order) { o.Status = order.Expired }), &after, after, false},
		// An order expires only after no payment was found for a while past its deadline, so a payment
		// on time to an expired order was indexed later than that.
		{"expired, on time", with(func(o *order.Order) { o.Status = order.Expired }), &before, after.Add(time.Hour), true},
		{"refunded", with(func(o *order.Order) { o.Status, o.ReceivedLamports = order.Refunded, 4_000_000 }), &before, before, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := payment.Decide(tt.o, candidate(10_000_000, tt.at), tt.now)
			require.NoError(t, err)
			var bt any
			if tt.at != nil {
				bt = tt.at.Format(time.RFC3339Nano)
			}
			assert.Equal(t, payment.Decision{
				Payment: row(payment.Late, 10_000_000, tt.at), Status: tt.o.Status, Received: tt.o.ReceivedLamports, PaidAt: tt.o.PaidAt,
				Events: []audit.Event{seen(tt.at), ev(audit.PaymentLate, "payment does not count towards the order", map[string]any{
					"lamports": int64(10_000_000), "block_time": bt, "expires_at": "2026-11-04T12:15:00Z", "on_time": tt.onTime,
				})},
			}, got, "recorded, and the order unchanged")
		})
	}
}

func TestDecideRecordsARejectedTransaction(t *testing.T) {
	tests := []struct {
		name     string
		o        order.Order
		verdict  payment.Verdict
		err      string
		lamports int64
		data     map[string]any
	}{
		{"failed on chain", open, payment.Verdict{Reason: payment.TxFailed}, "", 0,
			map[string]any{"reason": "tx_failed"}},
		{"no matching transfer", open, payment.Verdict{Reason: payment.NoMatchingTransfer}, "", 0,
			map[string]any{"reason": "no_matching_transfer"}},
		{"more claimed than received", open, payment.Verdict{Lamports: 10_000_000, Reason: payment.BalanceMismatch}, "", 10_000_000,
			map[string]any{"reason": "balance_mismatch"}},
		{"unreadable", open, payment.Verdict{Reason: payment.Unfetchable}, "unsupported transaction version", 0,
			map[string]any{"reason": "unfetchable", "error": "unsupported transaction version"}},
		{"to an expired order", with(func(o *order.Order) { o.Status = order.Expired }), payment.Verdict{Reason: payment.TxFailed}, "", 0,
			map[string]any{"reason": "tx_failed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := payment.Candidate{Signature: sig, Slot: 42, BlockTime: &before, Verdict: tt.verdict, Err: tt.err}
			got, err := payment.Decide(tt.o, c, before)
			require.NoError(t, err)
			want := row(payment.Rejected, tt.lamports, &before)
			want.Reason = tt.verdict.Reason
			assert.Equal(t, payment.Decision{
				Payment: want, Status: tt.o.Status, Received: tt.o.ReceivedLamports,
				Events: []audit.Event{seen(&before), ev(audit.PaymentInvalidTx, "transaction is not a payment to the order", tt.data)},
			}, got)
		})
	}
}

// Verify never calls a transaction valid unless it pays something, so a valid verdict of 0 or less
// is a bug, whatever the order and the time.
func TestDecideRefusesAValidVerdictThatPaysNothing(t *testing.T) {
	for _, status := range order.Statuses() {
		for _, at := range []*time.Time{&before, &after} {
			for _, lamports := range []int64{0, -1} {
				t.Run(fmt.Sprintf("%s at %s paying %d", status, at.Format(time.TimeOnly), lamports), func(t *testing.T) {
					_, err := payment.Decide(with(func(o *order.Order) { o.Status = status }), candidate(lamports, at), *at)
					assert.ErrorContains(t, err, "must pay a positive amount")
				})
			}
		}
	}
}

func TestDecideRefusesACreditThatOverflows(t *testing.T) {
	for _, status := range []order.Status{order.Underpaid, order.Paid} {
		t.Run(string(status), func(t *testing.T) {
			o := with(func(o *order.Order) {
				o.Status, o.AmountLamports, o.ReceivedLamports = status, math.MaxInt64, math.MaxInt64-1
			})
			_, err := payment.Decide(o, candidate(2, &before), before)
			assert.Error(t, err)
		})
	}
}

func TestDecideRefusesAnUnknownStatus(t *testing.T) {
	_, err := payment.Decide(with(func(o *order.Order) { o.Status = "lost" }), candidate(10_000_000, &before), before)
	assert.ErrorContains(t, err, `unknown status "lost"`)
}
