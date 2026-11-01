//go:build integration

package db_test

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/db"
)

// newOrder inserts a product and an order for it, and returns the order's id. Events that name an
// order need one, because of the foreign key.
func newOrder(t *testing.T) uuid.UUID {
	t.Helper()
	var product uuid.UUID
	err := pool.QueryRow(t.Context(),
		`INSERT INTO products (slug, title, price_lamports, file_name) VALUES ($1, 'Test', 1000, 'test.zip') RETURNING id`,
		"test-"+rand.Text(),
	).Scan(&product)
	require.NoError(t, err)
	id := uuid.NewV4()
	_, err = pool.Exec(t.Context(),
		`INSERT INTO orders (id, product_id, quantity, amount_lamports, reference, expires_at)
		 VALUES ($1, $2, 1, 1000, $3, $4)`,
		id, product, rand.Text(), time.Now().Add(15*time.Minute),
	)
	require.NoError(t, err)
	return id
}

func TestEventsRoundTrip(t *testing.T) {
	events := db.NewEvents(pool)
	order := newOrder(t)
	before := time.Now()
	require.NoError(t, events.InsertEvents(t.Context(),
		audit.Event{
			OrderID:   &order,
			Kind:      audit.PaymentOverpaid,
			Message:   "order received more than its amount",
			Signature: "sig-overpaid",
			Data:      map[string]any{"surplus_lamports": int64(9_007_199_254_740_993)}, // 2^53 + 1
		},
		audit.Event{OrderID: &order, Kind: audit.OrderCreated, Message: "order created", Data: map[string]any{}},
	))

	got, err := events.List(t.Context(), audit.ListFilter{OrderID: &order})
	require.NoError(t, err)
	require.Len(t, got, 2)
	created, overpaid := got[0], got[1] // newest first
	assert.Greater(t, created.ID, overpaid.ID)

	assert.Equal(t, audit.PaymentOverpaid, overpaid.Kind)
	assert.Equal(t, "order received more than its amount", overpaid.Message)
	assert.Equal(t, "sig-overpaid", overpaid.Signature)
	require.NotNil(t, overpaid.OrderID)
	assert.Equal(t, order, *overpaid.OrderID)
	assert.Equal(t, map[string]any{"surplus_lamports": json.Number("9007199254740993")}, overpaid.Data,
		"a number above 2^53 keeps every digit")
	assert.WithinDuration(t, before, overpaid.CreatedAt, time.Minute)

	assert.Equal(t, audit.OrderCreated, created.Kind)
	assert.Empty(t, created.Signature)
	assert.Nil(t, created.Data)

	var level string
	var nulls bool
	require.NoError(t, pool.QueryRow(t.Context(),
		`SELECT level, signature IS NULL AND data IS NULL FROM payment_events WHERE id = $1`, created.ID,
	).Scan(&level, &nulls))
	assert.Equal(t, "info", level, "the kind's level is stored")
	assert.True(t, nulls, "an empty signature and no data are stored as NULL")
	require.NoError(t, pool.QueryRow(t.Context(),
		`SELECT level FROM payment_events WHERE id = $1`, overpaid.ID).Scan(&level))
	assert.Equal(t, "warn", level)
}

func TestEventsWithoutAnOrder(t *testing.T) {
	events := db.NewEvents(pool)
	msg := "getSignaturesForAddress failed " + rand.Text()
	require.NoError(t, events.InsertEvents(t.Context(), audit.Event{Kind: audit.RPCError, Message: msg}))

	got, err := events.List(t.Context(), audit.ListFilter{Level: audit.Error})
	require.NoError(t, err)
	require.NotEmpty(t, got)
	assert.Equal(t, msg, got[0].Message, "the newest error event")
	assert.Nil(t, got[0].OrderID)
}

func TestEventsListFilters(t *testing.T) {
	events := db.NewEvents(pool)
	a, b := newOrder(t), newOrder(t)
	require.NoError(t, events.InsertEvents(t.Context(),
		audit.Event{OrderID: &a, Kind: audit.OrderCreated, Message: "a created"},
		audit.Event{OrderID: &a, Kind: audit.PaymentUnderpaid, Message: "a underpaid"},
		audit.Event{OrderID: &b, Kind: audit.OrderCreated, Message: "b created"},
		audit.Event{OrderID: &a, Kind: audit.PaymentLate, Message: "a late"},
	))

	messages := func(f audit.ListFilter) []string {
		t.Helper()
		got, err := events.List(t.Context(), f)
		require.NoError(t, err)
		var out []string
		for _, ev := range got {
			out = append(out, ev.Message)
		}
		return out
	}
	assert.Equal(t, []string{"a late", "a underpaid", "a created"}, messages(audit.ListFilter{OrderID: &a}))
	assert.Equal(t, []string{"b created"}, messages(audit.ListFilter{OrderID: &b}))
	assert.Equal(t, []string{"a late", "a underpaid"}, messages(audit.ListFilter{OrderID: &a, Level: audit.Warn}))
	assert.Empty(t, messages(audit.ListFilter{OrderID: &a, Level: audit.Error}))

	all, err := events.List(t.Context(), audit.ListFilter{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(all), 4)
	assert.Equal(t, "a late", all[0].Message, "no filter lists every order's events, newest first")
	assert.Equal(t, "b created", all[1].Message)
}

func TestEventsListPages(t *testing.T) {
	events := db.NewEvents(pool)
	order := newOrder(t)
	const total = db.EventsPageSize + 5
	var evs []audit.Event
	for i := range total {
		evs = append(evs, audit.Event{OrderID: &order, Kind: audit.PaymentSeen, Message: fmt.Sprint(i)})
	}
	require.NoError(t, events.InsertEvents(t.Context(), evs...))

	first, err := events.List(t.Context(), audit.ListFilter{OrderID: &order})
	require.NoError(t, err)
	require.Len(t, first, db.EventsPageSize)
	second, err := events.List(t.Context(), audit.ListFilter{OrderID: &order, Before: first[len(first)-1].ID})
	require.NoError(t, err)
	require.Len(t, second, 5)

	var got []string
	pages := append(first, second...)
	for i, ev := range pages {
		got = append(got, ev.Message)
		if i > 0 {
			assert.Less(t, ev.ID, pages[i-1].ID, "ids fall from one event to the next")
		}
	}
	for i := range total {
		assert.Equal(t, fmt.Sprint(total-1-i), got[i], "newest first, with nothing lost or repeated across pages")
	}

	last, err := events.List(t.Context(), audit.ListFilter{OrderID: &order, Before: second[len(second)-1].ID})
	require.NoError(t, err)
	assert.Empty(t, last)
}

func TestInsertEventsSavesAllOrNone(t *testing.T) {
	events := db.NewEvents(pool)
	order := newOrder(t)
	missing := uuid.NewV4()
	err := events.InsertEvents(t.Context(),
		audit.Event{OrderID: &order, Kind: audit.PaymentSeen, Message: "saved, then rolled back"},
		audit.Event{OrderID: &missing, Kind: audit.PaymentSeen, Message: "no such order"},
	)
	require.ErrorContains(t, err, "insert a payment.seen event")

	err = events.InsertEvents(t.Context(),
		audit.Event{OrderID: &order, Kind: audit.PaymentSeen, Message: "saved, then rolled back"},
		audit.Event{OrderID: &order, Kind: audit.PaymentSeen, Message: "bad data", Data: map[string]any{"c": make(chan int)}},
	)
	require.ErrorContains(t, err, "encode the data of a payment.seen event")

	got, err := events.List(t.Context(), audit.ListFilter{OrderID: &order})
	require.NoError(t, err)
	assert.Empty(t, got, "a failed insert leaves none of its events")
}
