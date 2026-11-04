//go:build integration

package db_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/db"
	"github.com/aleksiuno/solpay-shop/internal/db/sqlcdb"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
)

// newPayments returns a Payments whose recorder logs to the returned buffer.
func newPayments() (*db.Payments, *bytes.Buffer) {
	var buf bytes.Buffer
	rec := audit.NewRecorder(db.NewEvents(pool), slog.New(slog.NewJSONHandler(&buf, nil)))
	return db.NewPayments(pool, rec), &buf
}

// paying returns a decide function for Apply that decides, with payment.Decide, what a valid
// transaction with signature sig paying lamports does, and notes each order it is given.
func paying(sig string, lamports int64, seen *[]order.Order) func(order.Order) (payment.Decision, error) {
	return func(o order.Order) (payment.Decision, error) {
		if seen != nil {
			*seen = append(*seen, o)
		}
		at := testNow()
		c := payment.Candidate{Signature: sig, Slot: 7, BlockTime: &at, Verdict: payment.Verdict{Lamports: lamports}}
		return payment.Decide(o, c, at)
	}
}

func newSignature() string { return "sig-" + rand.Text() }

// ledgerRow is a payments row as the test reads it back.
type ledgerRow struct {
	OrderID   uuid.UUID
	Outcome   string
	Lamports  int64
	Slot      int64
	BlockTime *time.Time
	Reason    *string
}

func ledger(t *testing.T, sig string) (ledgerRow, bool) {
	t.Helper()
	var r ledgerRow
	err := pool.QueryRow(t.Context(), `SELECT order_id, outcome, lamports, slot, block_time, reason
		FROM payments WHERE signature = $1`, sig).Scan(&r.OrderID, &r.Outcome, &r.Lamports, &r.Slot, &r.BlockTime, &r.Reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return ledgerRow{}, false
	}
	require.NoError(t, err)
	return r, true
}

func getOrder(t *testing.T, id uuid.UUID) order.Order {
	t.Helper()
	orders, _ := newOrders()
	o, err := orders.Get(t.Context(), id)
	require.NoError(t, err)
	return o
}

func TestPaymentsApplyCredits(t *testing.T) {
	payments, log := newPayments()
	o := storedOrder(t, order.Pending)
	sig := newSignature()
	applied, err := payments.Apply(t.Context(), o.ID, paying(sig, o.AmountLamports, nil))
	require.NoError(t, err)
	assert.True(t, applied)

	got := getOrder(t, o.ID)
	assert.Equal(t, order.Paid, got.Status)
	assert.Equal(t, o.AmountLamports, got.ReceivedLamports)
	assert.NotNil(t, got.PaidAt)
	row, ok := ledger(t, sig)
	require.True(t, ok)
	assert.Equal(t, ledgerRow{OrderID: o.ID, Outcome: "credited", Lamports: o.AmountLamports, Slot: 7, BlockTime: row.BlockTime}, row)
	require.NotNil(t, row.BlockTime)
	assert.True(t, row.BlockTime.Equal(*got.PaidAt), "paid at the payment's block time")
	assert.Equal(t, []audit.Kind{audit.PaymentVerified, audit.PaymentSeen}, eventKinds(t, o.ID))
	assert.Contains(t, log.String(), `"kind":"payment.verified"`, "logged after the commit")
}

// The same transaction applied again, as concurrent callers or a restart can do, is credited once.
func TestPaymentsApplyTheSameSignatureAgain(t *testing.T) {
	payments, log := newPayments()
	o := storedOrder(t, order.Pending)
	sig := newSignature()
	var seen []order.Order
	applied, err := payments.Apply(t.Context(), o.ID, paying(sig, 1_000, &seen))
	require.NoError(t, err)
	assert.True(t, applied)
	applied, err = payments.Apply(t.Context(), o.ID, paying(sig, 1_000, &seen))
	require.NoError(t, err)
	assert.False(t, applied)

	got := getOrder(t, o.ID)
	assert.Equal(t, order.Underpaid, got.Status)
	assert.Equal(t, int64(1_000), got.ReceivedLamports, "credited once")
	require.Len(t, seen, 2)
	assert.Equal(t, int64(1_000), seen[1].ReceivedLamports, "the second apply reads the first one's credit")
	assert.Equal(t, []audit.Kind{audit.PaymentDuplicateIgnored, audit.PaymentUnderpaid, audit.PaymentSeen}, eventKinds(t, o.ID))
	assert.Contains(t, log.String(), `"kind":"payment.duplicate_ignored"`, "logged after its commit too")
}

// A signature is the ledger's key across all orders, so a transaction that lists two orders'
// references pays at most one of them.
func TestPaymentsSignatureCountsForOneOrderOnly(t *testing.T) {
	payments, _ := newPayments()
	first, second := storedOrder(t, order.Pending), storedOrder(t, order.Pending)
	sig := newSignature()
	_, err := payments.Apply(t.Context(), first.ID, paying(sig, first.AmountLamports, nil))
	require.NoError(t, err)
	applied, err := payments.Apply(t.Context(), second.ID, paying(sig, second.AmountLamports, nil))
	require.NoError(t, err)
	assert.False(t, applied)

	got := getOrder(t, second.ID)
	assert.Equal(t, order.Pending, got.Status)
	assert.Zero(t, got.ReceivedLamports)
	row, _ := ledger(t, sig)
	assert.Equal(t, first.ID, row.OrderID)
	assert.Equal(t, []audit.Kind{audit.PaymentDuplicateIgnored}, eventKinds(t, second.ID))

	_, err = pool.Exec(t.Context(), `INSERT INTO payments (signature, order_id, outcome, lamports, slot)
		VALUES ($1, $2, 'credited', 1, 1)`, sig, second.ID)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, "the schema refuses a second row for the signature")
	assert.Equal(t, "23505", pgErr.Code)
}

func TestPaymentsConcurrentAppliesCreditOnce(t *testing.T) {
	payments, _ := newPayments()
	o := storedOrder(t, order.Pending)
	sig := newSignature()
	const n = 8
	results := make(chan bool, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			applied, err := payments.Apply(t.Context(), o.ID, paying(sig, 1_000, nil))
			assert.NoError(t, err)
			results <- applied
		})
	}
	wg.Wait()
	close(results)

	appliedCount := 0
	for applied := range results {
		if applied {
			appliedCount++
		}
	}
	assert.Equal(t, 1, appliedCount)
	got := getOrder(t, o.ID)
	assert.Equal(t, int64(1_000), got.ReceivedLamports)
	var rows int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM payments WHERE order_id = $1`, o.ID).Scan(&rows))
	assert.Equal(t, 1, rows)
	kinds := eventKinds(t, o.ID)
	assert.Len(t, kinds, n+1, "seen and underpaid once, and a duplicate_ignored for each other apply")
}

// Different transactions for one order, applied at once, all count: the row lock makes each apply
// read the total that the one before it stored, so no credit overwrites another.
func TestPaymentsConcurrentTopUpsAllCount(t *testing.T) {
	payments, _ := newPayments()
	o := storedOrder(t, order.Underpaid)
	const n = 8
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			applied, err := payments.Apply(t.Context(), o.ID, paying(newSignature(), 1_000, nil))
			if assert.NoError(t, err) {
				assert.True(t, applied)
			}
		})
	}
	wg.Wait()

	got := getOrder(t, o.ID)
	assert.Equal(t, order.Underpaid, got.Status)
	assert.Equal(t, int64(n*1_000), got.ReceivedLamports)
}

// Rejected and late transactions are recorded with their outcome, and change nothing.
func TestPaymentsApplyRecordsWithoutCrediting(t *testing.T) {
	payments, _ := newPayments()
	o := storedOrder(t, order.Expired)
	rejected, late := newSignature(), newSignature()

	_, err := payments.Apply(t.Context(), o.ID, func(o order.Order) (payment.Decision, error) {
		c := payment.Candidate{Signature: rejected, Slot: 9, Verdict: payment.Verdict{Reason: payment.Unfetchable}, Err: "decode failed"}
		return payment.Decide(o, c, testNow())
	})
	require.NoError(t, err)
	_, err = payments.Apply(t.Context(), o.ID, paying(late, 1_000, nil))
	require.NoError(t, err)

	got := getOrder(t, o.ID)
	assert.Equal(t, order.Expired, got.Status)
	assert.Zero(t, got.ReceivedLamports)
	assert.True(t, got.HasLatePayment)
	reason := "unfetchable"
	row, _ := ledger(t, rejected)
	assert.Equal(t, ledgerRow{OrderID: o.ID, Outcome: "rejected", Slot: 9, Reason: &reason}, row, "no block time stored as NULL")
	row, _ = ledger(t, late)
	assert.Equal(t, "late", row.Outcome)
	assert.Equal(t, int64(1_000), row.Lamports)
}

func TestPaymentsApplyRollsBack(t *testing.T) {
	payments, log := newPayments()
	o := storedOrder(t, order.Pending)
	sig := newSignature()

	refused := errors.New("refused")
	_, err := payments.Apply(t.Context(), o.ID, func(order.Order) (payment.Decision, error) { return payment.Decision{}, refused })
	require.Same(t, refused, err, "decide's error comes back as it is")

	_, err = payments.Apply(t.Context(), o.ID, func(o order.Order) (payment.Decision, error) {
		d, err := paying(sig, o.AmountLamports, nil)(o)
		d.Events = append(d.Events, audit.Event{OrderID: &o.ID, Kind: audit.PaymentSeen, Data: map[string]any{"c": make(chan int)}})
		return d, err
	})
	require.ErrorContains(t, err, "encode the data", "an event that cannot be saved")

	got := getOrder(t, o.ID)
	assert.Equal(t, order.Pending, got.Status, "nothing is stored")
	assert.Zero(t, got.ReceivedLamports)
	_, ok := ledger(t, sig)
	assert.False(t, ok, "not even the ledger row")
	assert.Empty(t, eventKinds(t, o.ID))
	assert.Empty(t, log.String())
}

func TestPaymentsApplyChecksTheMoveItself(t *testing.T) {
	payments, _ := newPayments()
	o := storedOrder(t, order.Expired)
	sig := newSignature()
	_, err := payments.Apply(t.Context(), o.ID, func(o order.Order) (payment.Decision, error) {
		return payment.Decision{
			Payment: payment.Payment{Signature: sig, OrderID: o.ID, Outcome: payment.Credited, Lamports: 1_000, Slot: 1},
			Status:  order.Paid, Received: 1_000,
		}, nil
	})
	require.ErrorIs(t, err, order.ErrInvalidTransition, "a late payment never pays an expired order")
	assert.Equal(t, order.Expired, getOrder(t, o.ID).Status)
	_, ok := ledger(t, sig)
	assert.False(t, ok)
}

func TestPaymentsApplyRefusesADecisionForAnotherOrder(t *testing.T) {
	payments, _ := newPayments()
	o, other := storedOrder(t, order.Pending), storedOrder(t, order.Pending)
	_, err := payments.Apply(t.Context(), o.ID, func(order.Order) (payment.Decision, error) {
		return payment.Decide(other, payment.Candidate{Signature: newSignature(), Verdict: payment.Verdict{Lamports: 1}}, testNow())
	})
	assert.ErrorContains(t, err, "applied to order")
}

func TestPaymentsApplyUnknownOrder(t *testing.T) {
	payments, _ := newPayments()
	_, err := payments.Apply(t.Context(), uuid.NewV4(), paying(newSignature(), 1, nil))
	assert.ErrorIs(t, err, order.ErrNotFound)
}

// A signature counts as known whichever order it was applied to.
func TestPaymentsKnownSignatures(t *testing.T) {
	payments, _ := newPayments()
	first, second := newSignature(), newSignature()
	for _, sig := range []string{first, second} {
		o := storedOrder(t, order.Pending)
		_, err := payments.Apply(t.Context(), o.ID, paying(sig, 1_000, nil))
		require.NoError(t, err)
	}

	got, err := payments.KnownSignatures(t.Context(), []string{first, newSignature(), second})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{first: true, second: true}, got)

	got, err = payments.KnownSignatures(t.Context(), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestUpdateOrderPaymentChangesOnlyTheStatusItWasGiven(t *testing.T) {
	o := storedOrder(t, order.Paid)
	q := sqlcdb.New(pool)
	params := sqlcdb.UpdateOrderPaymentParams{
		ID: o.ID, FromStatus: string(order.Pending), ToStatus: string(order.Underpaid), ReceivedLamports: 1_000,
	}
	n, err := q.UpdateOrderPayment(t.Context(), params)
	require.NoError(t, err)
	assert.Zero(t, n, "a stale status changes no row")
	got := getOrder(t, o.ID)
	assert.Equal(t, order.Paid, got.Status)
	assert.Zero(t, got.ReceivedLamports)

	params.FromStatus, params.ToStatus = string(order.Paid), string(order.Paid)
	n, err = q.UpdateOrderPayment(t.Context(), params)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	assert.Equal(t, int64(1_000), getOrder(t, o.ID).ReceivedLamports)
}
