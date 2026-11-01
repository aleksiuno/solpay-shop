//go:build integration

package db_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/db"
	"github.com/aleksiuno/solpay-shop/internal/db/sqlcdb"
	"github.com/aleksiuno/solpay-shop/internal/order"
)

// testNow returns the current time as the order service expects it: UTC, truncated to the
// microseconds Postgres stores.
func testNow() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// newOrders returns an Orders whose recorder logs to the returned buffer.
func newOrders() (*db.Orders, *bytes.Buffer) {
	var buf bytes.Buffer
	rec := audit.NewRecorder(db.NewEvents(pool), slog.New(slog.NewJSONHandler(&buf, nil)))
	return db.NewOrders(pool, rec), &buf
}

// pendingOrder returns a new pending order for a new product, not yet stored.
func pendingOrder(t *testing.T) order.Order {
	t.Helper()
	p, err := db.NewProducts(pool).Create(t.Context(), newProduct())
	require.NoError(t, err)
	now := testNow()
	return order.Order{
		ID: uuid.NewV4(), ProductID: p.ID, ProductSlug: p.Slug, ProductTitle: p.Title,
		Quantity: 2, AmountLamports: 2 * p.PriceLamports, Reference: rand.Text(),
		Status: order.Pending, ExpiresAt: now.Add(15 * time.Minute), CreatedAt: now,
	}
}

// storedOrder stores a pending order and then sets its status with a plain UPDATE, which skips
// Transition, so that a test can start from any status.
func storedOrder(t *testing.T, s order.Status) order.Order {
	t.Helper()
	o := pendingOrder(t)
	orders, _ := newOrders()
	require.NoError(t, orders.Create(t.Context(), o))
	_, err := pool.Exec(t.Context(), `UPDATE orders SET status = $2 WHERE id = $1`, o.ID, string(s))
	require.NoError(t, err)
	o.Status = s
	return o
}

// execer runs a statement on the pool or in a transaction.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// addPayment stores a ledger row with outcome for the order with id.
func addPayment(t *testing.T, x execer, id uuid.UUID, outcome string) {
	t.Helper()
	_, err := x.Exec(t.Context(), `INSERT INTO payments (signature, order_id, outcome, lamports, slot, reason)
		VALUES ($1, $2, $3, 1000, 1, CASE WHEN $3 = 'rejected' THEN 'tx_failed' END)`,
		"sig-"+rand.Text(), id, outcome)
	require.NoError(t, err)
}

func createdEvent(o order.Order) audit.Event {
	return audit.Event{OrderID: &o.ID, Kind: audit.OrderCreated, Message: "order created"}
}

// eventKinds returns the kinds of the order's stored events, newest first.
func eventKinds(t *testing.T, id uuid.UUID) []audit.Kind {
	t.Helper()
	evs, err := db.NewEvents(pool).List(t.Context(), audit.ListFilter{OrderID: &id})
	require.NoError(t, err)
	var kinds []audit.Kind
	for _, ev := range evs {
		kinds = append(kinds, ev.Kind)
	}
	return kinds
}

// assertSameOrder compares two orders with their times in UTC, because pgx reads times in the
// local zone.
func assertSameOrder(t *testing.T, want, got order.Order) {
	t.Helper()
	assert.Equal(t, inUTC(want), inUTC(got))
}

func inUTC(o order.Order) order.Order {
	o.ExpiresAt, o.CreatedAt = o.ExpiresAt.UTC(), o.CreatedAt.UTC()
	for _, at := range []**time.Time{&o.PaidAt, &o.FulfilledAt, &o.RefundedAt} {
		if *at != nil {
			utc := (*at).UTC()
			*at = &utc
		}
	}
	return o
}

// listed returns the order with id from the newest orders that List returns.
func listed(t *testing.T, orders *db.Orders, id uuid.UUID) order.Order {
	t.Helper()
	all, err := orders.List(t.Context(), order.ListFilter{})
	require.NoError(t, err)
	i := slices.IndexFunc(all, func(o order.Order) bool { return o.ID == id })
	require.GreaterOrEqual(t, i, 0, "the order is among the newest")
	return all[i]
}

// only returns the ids of the orders in list that are among ids, in list order.
func only(list []order.Order, ids ...uuid.UUID) []uuid.UUID {
	var out []uuid.UUID
	for _, o := range list {
		if slices.Contains(ids, o.ID) {
			out = append(out, o.ID)
		}
	}
	return out
}

// waitForLockWait waits until a statement in the test database is waiting for a lock.
func waitForLockWait(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting bool
		err := pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock')`).Scan(&waiting)
		return err == nil && waiting
	}, 10*time.Second, 10*time.Millisecond, "a statement waits for the order's row lock")
}

func TestOrdersCreateAndGet(t *testing.T) {
	orders, log := newOrders()
	o := pendingOrder(t)
	require.NoError(t, orders.Create(t.Context(), o, createdEvent(o)))

	got, err := orders.Get(t.Context(), o.ID)
	require.NoError(t, err)
	assertSameOrder(t, o, got)
	assert.Equal(t, []audit.Kind{audit.OrderCreated}, eventKinds(t, o.ID), "the event is stored with the order")
	assert.Contains(t, log.String(), `"kind":"order.created"`, "and logged after the commit")

	var due bool
	require.NoError(t, pool.QueryRow(t.Context(),
		`SELECT next_poll_at = created_at FROM orders WHERE id = $1`, o.ID).Scan(&due))
	assert.True(t, due, "a new order is due for its first poll at once")

	_, err = orders.Get(t.Context(), uuid.NewV4())
	require.ErrorIs(t, err, order.ErrNotFound)
}

func TestOrdersCreateSavesAllOrNothing(t *testing.T) {
	orders, log := newOrders()
	o := pendingOrder(t)
	bad := createdEvent(o)
	bad.Data = map[string]any{"c": make(chan int)}
	require.Error(t, orders.Create(t.Context(), o, bad))
	_, err := orders.Get(t.Context(), o.ID)
	require.ErrorIs(t, err, order.ErrNotFound, "an event that cannot be saved leaves no order")
	assert.Empty(t, log.String(), "nothing is logged for a rollback")

	first := pendingOrder(t)
	require.NoError(t, orders.Create(t.Context(), first))
	second := pendingOrder(t)
	second.Reference = first.Reference
	require.Error(t, orders.Create(t.Context(), second, createdEvent(second)), "references are unique")
	assert.Empty(t, eventKinds(t, second.ID), "an order that cannot be saved leaves no event")
	assert.Empty(t, log.String(), "nothing is logged for a rollback")
}

func TestOrdersReadEveryField(t *testing.T) {
	orders, _ := newOrders()
	o := storedOrder(t, order.Fulfilled)
	paidAt, fulfilledAt := testNow().Add(-2*time.Minute), testNow().Add(-time.Minute)
	o.ReceivedLamports, o.PaidAt, o.FulfilledAt = o.AmountLamports+1, &paidAt, &fulfilledAt
	_, err := pool.Exec(t.Context(),
		`UPDATE orders SET received_lamports = $2, paid_at = $3, fulfilled_at = $4 WHERE id = $1`,
		o.ID, o.ReceivedLamports, paidAt, fulfilledAt)
	require.NoError(t, err)
	addPayment(t, pool, o.ID, "late")
	o.HasLatePayment = true

	got, err := orders.Get(t.Context(), o.ID)
	require.NoError(t, err)
	assertSameOrder(t, o, got)
	assertSameOrder(t, o, listed(t, orders, o.ID))

	stop := errors.New("stop")
	_, err = orders.Refund(t.Context(), o.ID, testNow(), func(locked order.Order) ([]audit.Event, error) {
		assertSameOrder(t, o, locked)
		return nil, stop
	})
	require.ErrorIs(t, err, stop)
}

func TestOrdersHasLatePayment(t *testing.T) {
	orders, _ := newOrders()
	o := storedOrder(t, order.Expired)
	s := order.NewService(orders, nil, time.Minute, rand.Text, testNow)

	addPayment(t, pool, storedOrder(t, order.Expired).ID, "late") // another order's
	addPayment(t, pool, o.ID, "credited")
	addPayment(t, pool, o.ID, "rejected")
	got, err := orders.Get(t.Context(), o.ID)
	require.NoError(t, err)
	assert.False(t, got.HasLatePayment, "neither a credited nor a rejected payment is late")
	assert.False(t, listed(t, orders, o.ID).HasLatePayment, "nor through List")
	_, err = s.Refund(t.Context(), o.ID, "")
	require.ErrorIs(t, err, order.ErrNoLatePayment)

	addPayment(t, pool, o.ID, "late")
	got, err = orders.Get(t.Context(), o.ID)
	require.NoError(t, err)
	assert.True(t, got.HasLatePayment)
	assert.True(t, listed(t, orders, o.ID).HasLatePayment)
	refunded, err := s.Refund(t.Context(), o.ID, "")
	require.NoError(t, err, "a late payment makes the expired order refundable")
	assert.Equal(t, order.Refunded, refunded.Status)
}

func TestOrdersList(t *testing.T) {
	orders, _ := newOrders()
	var ids []uuid.UUID // oldest first, since each order is created at the time it is stored
	for _, s := range []order.Status{order.Pending, order.Paid, order.Pending} {
		ids = append(ids, storedOrder(t, s).ID)
	}

	all, err := orders.List(t.Context(), order.ListFilter{})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(all), db.OrdersPageSize)
	assert.Equal(t, []uuid.UUID{ids[2], ids[1], ids[0]}, only(all, ids...), "newest first")

	paid, err := orders.List(t.Context(), order.ListFilter{Status: order.Paid})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{ids[1]}, only(paid, ids...))
	for _, o := range paid {
		assert.Equal(t, order.Paid, o.Status)
	}
}

func TestOrdersRefund(t *testing.T) {
	orders, log := newOrders()
	o := storedOrder(t, order.Paid)
	at := testNow()
	var checked order.Order
	got, err := orders.Refund(t.Context(), o.ID, at, func(locked order.Order) ([]audit.Event, error) {
		checked = locked
		return []audit.Event{{OrderID: &locked.ID, Kind: audit.OrderRefunded, Message: "order refunded"}}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, order.Paid, checked.Status, "check sees the order as it was")

	want := o
	want.Status, want.RefundedAt = order.Refunded, &at
	assertSameOrder(t, want, got)
	stored, err := orders.Get(t.Context(), o.ID)
	require.NoError(t, err)
	assertSameOrder(t, want, stored)
	assert.Equal(t, []audit.Kind{audit.OrderRefunded}, eventKinds(t, o.ID))
	assert.Contains(t, log.String(), `"kind":"order.refunded"`)
}

func TestOrdersRefundRollsBack(t *testing.T) {
	orders, log := newOrders()
	o := storedOrder(t, order.Paid)
	refused := errors.New("refused")
	_, err := orders.Refund(t.Context(), o.ID, testNow(), func(order.Order) ([]audit.Event, error) {
		return nil, refused
	})
	require.ErrorIs(t, err, refused, "check's error comes back as it is")

	_, err = orders.Refund(t.Context(), o.ID, testNow(), func(locked order.Order) ([]audit.Event, error) {
		return []audit.Event{{OrderID: &locked.ID, Kind: audit.OrderRefunded, Data: map[string]any{"c": make(chan int)}}}, nil
	})
	require.ErrorContains(t, err, "encode the data", "an event that cannot be saved")

	stored, err := orders.Get(t.Context(), o.ID)
	require.NoError(t, err)
	assert.Equal(t, order.Paid, stored.Status, "both failures leave the order as it was")
	assert.Nil(t, stored.RefundedAt)
	assert.Empty(t, eventKinds(t, o.ID))
	assert.Empty(t, log.String(), "nothing is logged for a rollback")
}

func TestOrdersRefundChecksTheMoveItself(t *testing.T) {
	orders, _ := newOrders()
	o := storedOrder(t, order.Pending)
	_, err := orders.Refund(t.Context(), o.ID, testNow(), func(order.Order) ([]audit.Event, error) {
		return nil, nil // a check that forgot the rules
	})
	require.ErrorIs(t, err, order.ErrInvalidTransition)
	stored, err := orders.Get(t.Context(), o.ID)
	require.NoError(t, err)
	assert.Equal(t, order.Pending, stored.Status)
}

func TestOrdersRefundUnknown(t *testing.T) {
	orders, _ := newOrders()
	called := false
	_, err := orders.Refund(t.Context(), uuid.NewV4(), testNow(), func(order.Order) ([]audit.Event, error) {
		called = true
		return nil, nil
	})
	require.ErrorIs(t, err, order.ErrNotFound)
	assert.False(t, called)
}

func TestRefundOrderChangesOnlyTheStatusItWasGiven(t *testing.T) {
	o := storedOrder(t, order.Paid)
	q := sqlcdb.New(pool)
	at := testNow()
	params := sqlcdb.RefundOrderParams{
		ID: o.ID, FromStatus: string(order.Fulfilled), ToStatus: string(order.Refunded), RefundedAt: &at,
	}
	n, err := q.RefundOrder(t.Context(), params)
	require.NoError(t, err)
	assert.Zero(t, n, "a stale status changes no row")

	params.FromStatus = string(order.Paid)
	n, err = q.RefundOrder(t.Context(), params)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestOrdersRefundWaitsForTheLock(t *testing.T) {
	orders, _ := newOrders()
	o := storedOrder(t, order.Paid)
	s := order.NewService(orders, nil, time.Minute, rand.Text, testNow)

	// The first refund holds the lock until release is closed.
	locked, release := make(chan struct{}), make(chan struct{})
	free := sync.OnceFunc(func() { close(release) })
	defer free()
	first := make(chan error, 1)
	go func() {
		_, err := orders.Refund(t.Context(), o.ID, testNow(), func(l order.Order) ([]audit.Event, error) {
			close(locked)
			<-release
			return []audit.Event{{OrderID: &l.ID, Kind: audit.OrderRefunded, Message: "order refunded"}}, nil
		})
		first <- err
	}()
	select {
	case <-locked:
	case err := <-first:
		t.Fatalf("the first refund returned before its check ran: %v", err)
	}
	second := make(chan error, 1)
	go func() {
		_, err := s.Refund(t.Context(), o.ID, "")
		second <- err
	}()
	waitForLockWait(t)
	free()

	require.NoError(t, <-first)
	require.ErrorIs(t, <-second, order.ErrInvalidTransition, "the second refund then reads a refunded order")
	assert.Equal(t, []audit.Kind{audit.OrderRefunded}, eventKinds(t, o.ID), "and one event")
}

func TestOrdersRefundSeesWhatCommittedWhileItWaited(t *testing.T) {
	orders, _ := newOrders()
	o := storedOrder(t, order.Expired)
	s := order.NewService(orders, nil, time.Minute, rand.Text, testNow)

	// Another transaction locks the order and records a late payment for it.
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	_, err = tx.Exec(t.Context(), `SELECT 1 FROM orders WHERE id = $1 FOR UPDATE`, o.ID)
	require.NoError(t, err)
	addPayment(t, tx, o.ID, "late")

	done := make(chan error, 1)
	go func() {
		_, err := s.Refund(t.Context(), o.ID, "")
		done <- err
	}()
	waitForLockWait(t)
	require.NoError(t, tx.Commit(t.Context()))
	require.NoError(t, <-done, "the refund reads the order after the lock, so it sees the late payment")
}
