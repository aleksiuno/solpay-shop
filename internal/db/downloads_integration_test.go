//go:build integration

package db_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/db"
	"github.com/aleksiuno/solpay-shop/internal/order"
)

// newDownloads returns a Downloads whose recorder logs to the returned buffer.
func newDownloads() (*db.Downloads, *bytes.Buffer) {
	var buf bytes.Buffer
	rec := audit.NewRecorder(db.NewEvents(pool), slog.New(slog.NewJSONHandler(&buf, nil)))
	return db.NewDownloads(pool, rec), &buf
}

func TestDownloadsGet(t *testing.T) {
	downloads, _ := newDownloads()
	o := storedOrder(t, order.Paid)
	got, p, err := downloads.Get(t.Context(), o.ID)
	require.NoError(t, err)
	assertSameOrder(t, o, got)
	assert.Equal(t, o.ProductID, p.ID)
	assert.Equal(t, o.ProductSlug, p.Slug)
	assert.Equal(t, "test.md", p.FileName)

	_, _, err = downloads.Get(t.Context(), uuid.NewV4())
	require.ErrorIs(t, err, order.ErrNotFound)
}

func TestDownloadsMarkFulfilled(t *testing.T) {
	downloads, log := newDownloads()
	o := storedOrder(t, order.Paid)
	at := testNow()
	fulfilled, err := downloads.MarkFulfilled(t.Context(), o.ID, at)
	require.NoError(t, err)
	assert.True(t, fulfilled)
	got := getOrder(t, o.ID)
	assert.Equal(t, order.Fulfilled, got.Status)
	require.NotNil(t, got.FulfilledAt)
	assert.WithinDuration(t, at, *got.FulfilledAt, 0)

	evs, err := db.NewEvents(pool).List(t.Context(), audit.ListFilter{OrderID: &o.ID})
	require.NoError(t, err)
	require.Len(t, evs, 1)
	assert.Equal(t, audit.DownloadIssued, evs[0].Kind)
	assert.Equal(t, "download issued", evs[0].Message)
	assert.Nil(t, evs[0].Data)
	assert.Contains(t, log.String(), `"kind":"download.issued"`)

	fulfilled, err = downloads.MarkFulfilled(t.Context(), o.ID, at.Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, fulfilled, "a fulfilled order is not fulfilled again")
	assert.WithinDuration(t, at, *getOrder(t, o.ID).FulfilledAt, 0, "the first download's time stays")
	assert.Len(t, eventKinds(t, o.ID), 1)
}

func TestDownloadsMarkFulfilledLeavesOtherOrdersAlone(t *testing.T) {
	for _, s := range []order.Status{order.Pending, order.Underpaid, order.Expired, order.Refunded} {
		downloads, log := newDownloads()
		o := storedOrder(t, s)
		fulfilled, err := downloads.MarkFulfilled(t.Context(), o.ID, testNow())
		require.NoError(t, err, s)
		assert.False(t, fulfilled, s)
		got := getOrder(t, o.ID)
		assert.Equal(t, s, got.Status)
		assert.Nil(t, got.FulfilledAt, s)
		assert.Empty(t, eventKinds(t, o.ID), s)
		assert.Empty(t, log.String(), s)
	}

	downloads, _ := newDownloads()
	fulfilled, err := downloads.MarkFulfilled(t.Context(), uuid.NewV4(), testNow())
	require.NoError(t, err)
	assert.False(t, fulfilled, "an unknown order")
}

// If the event cannot be stored, the order stays paid and nothing is logged. A constraint that
// refuses the order's events makes the insert fail.
func TestDownloadsMarkFulfilledRollsBack(t *testing.T) {
	downloads, log := newDownloads()
	o := storedOrder(t, order.Paid)
	name := "refuse_" + strings.ToLower(rand.Text())
	_, err := pool.Exec(t.Context(), fmt.Sprintf(
		`ALTER TABLE payment_events ADD CONSTRAINT %s CHECK (order_id <> '%s') NOT VALID`, name, o.ID))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `ALTER TABLE payment_events DROP CONSTRAINT `+name)
		assert.NoError(t, err)
	})

	fulfilled, err := downloads.MarkFulfilled(t.Context(), o.ID, testNow())
	require.ErrorContains(t, err, name)
	assert.False(t, fulfilled)
	got := getOrder(t, o.ID)
	assert.Equal(t, order.Paid, got.Status)
	assert.Nil(t, got.FulfilledAt)
	assert.Empty(t, eventKinds(t, o.ID))
	assert.Empty(t, log.String(), "nothing is logged for a rollback")
}

// Of several first downloads at once, one fulfils the order and stores download.issued: the others
// find it no longer paid.
func TestDownloadsConcurrentFirstDownloadsFulfilOnce(t *testing.T) {
	downloads, _ := newDownloads()
	o := storedOrder(t, order.Paid)
	var fulfilled atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			ok, err := downloads.MarkFulfilled(t.Context(), o.ID, testNow())
			if assert.NoError(t, err) && ok {
				fulfilled.Add(1)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), fulfilled.Load())
	assert.Equal(t, []audit.Kind{audit.DownloadIssued}, eventKinds(t, o.ID))
}
