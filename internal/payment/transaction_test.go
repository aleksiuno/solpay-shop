package payment_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
	"github.com/aleksiuno/solpay-shop/internal/solana"
	"github.com/aleksiuno/solpay-shop/internal/solana/solanatest"
)

var _ payment.Blockhasher = (*solana.Client)(nil)

// node is an RPC node whose LatestBlockhash takes a second, on the clock of the synctest bubble it
// runs in, and returns a new blockhash on every call, or the error that fail holds for the next
// one.
type node struct {
	mu        sync.Mutex
	fail      error
	deadlines []time.Duration // how long each call's context had before its deadline
}

func (n *node) LatestBlockhash(ctx context.Context) (string, error) {
	d, _ := ctx.Deadline()
	n.mu.Lock()
	n.deadlines = append(n.deadlines, time.Until(d))
	call, err := len(n.deadlines), n.fail
	n.fail = nil
	n.mu.Unlock()
	time.Sleep(time.Second)
	if err != nil {
		return "", err
	}
	return blockhash(call), nil
}

func (n *node) calls() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.deadlines)
}

// newTransactions returns Transactions over n, timed by now, and the buffer they log to.
func newTransactions(n *node, now func() time.Time) (*payment.Transactions, *bytes.Buffer) {
	var buf bytes.Buffer
	return payment.NewTransactions(n, merchant, now, slog.New(slog.NewJSONHandler(&buf, nil))), &buf
}

// blockhash is the blockhash that node's nth call returns.
func blockhash(n int) string { return solanatest.Key(fmt.Sprintf("blockhash-%d", n)) }

// underpaid is an order for two licences that has received 0.004 SOL of its 0.01.
var underpaid = order.Order{
	ID: uuid.MustParse("8f2c4b6d-1a3e-4c5f-9b7d-0e2a4c6e8f1b"), ProductTitle: "Go payments checklist", Quantity: 2,
	AmountLamports: 10_000_000, ReceivedLamports: 4_000_000, Reference: reference, Status: order.Underpaid,
}

// paysTheRest returns the transaction with which payer pays the 0.006 SOL that underpaid still
// asks for, naming the blockhash of node's nth call.
func paysTheRest(t *testing.T, n int) []byte {
	t.Helper()
	tx, err := solana.TransferTransaction(solana.Transfer{
		Payer: payer, Recipient: merchant, Lamports: 6_000_000, References: []string{reference}, Blockhash: blockhash(n),
	})
	require.NoError(t, err)
	return tx
}

func TestBuildPaysWhatTheRequestAsksFor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		txs, _ := newTransactions(&node{}, time.Now)
		tx, msg, err := txs.Build(t.Context(), underpaid, payer)
		require.NoError(t, err)
		assert.Equal(t, paysTheRest(t, 1), tx, "what remains, to the merchant, with the reference")
		assert.Equal(t, "solpay-shop (devnet): Go payments checklist (2 licences)", msg)
	})
}

// Callers that arrive while a blockhash is being fetched wait for that fetch and share its
// blockhash.
func TestBuildFetchesOneBlockhashForConcurrentCallers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := &node{}
		txs, _ := newTransactions(n, time.Now)
		got := make([][]byte, 10)
		var wg sync.WaitGroup
		for i := range got {
			wg.Go(func() {
				tx, _, err := txs.Build(t.Context(), underpaid, payer)
				assert.NoError(t, err)
				got[i] = tx
			})
		}
		synctest.Wait()
		assert.Equal(t, 1, n.calls(), "one fetch under way for all ten")
		wg.Wait()
		assert.Equal(t, slices.Repeat([][]byte{paysTheRest(t, 1)}, 10), got)
	})
}

// A blockhash is handed out for 10 s from the start of its fetch, and then fetched again.
func TestBuildFetchesANewBlockhashAfterTenSeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := &node{}
		txs, _ := newTransactions(n, time.Now)
		build := func() []byte {
			tx, _, err := txs.Build(t.Context(), underpaid, payer)
			require.NoError(t, err)
			return tx
		}

		assert.Equal(t, paysTheRest(t, 1), build(), "a second later, when the fetch returns")
		time.Sleep(9*time.Second - time.Nanosecond)
		assert.Equal(t, paysTheRest(t, 1), build(), "just under 10 s after the fetch began")
		assert.Equal(t, 1, n.calls())
		time.Sleep(time.Nanosecond)
		assert.Equal(t, paysTheRest(t, 2), build(), "10 s after")
		assert.Equal(t, 2, n.calls())
	})
}

// A failed fetch is not kept: the next caller fetches again. The failure is logged.
func TestBuildFetchesAgainAfterAFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := &node{fail: errBoom}
		txs, buf := newTransactions(n, time.Now)
		_, _, err := txs.Build(t.Context(), underpaid, payer)
		require.ErrorIs(t, err, payment.ErrNoBlockhash)
		require.ErrorIs(t, err, errBoom)
		assert.Contains(t, buf.String(), `"level":"ERROR","msg":"no blockhash from the RPC node","error":"boom"}`)

		tx, _, err := txs.Build(t.Context(), underpaid, payer)
		require.NoError(t, err)
		assert.Equal(t, paysTheRest(t, 2), tx)
		assert.Equal(t, 2, n.calls())
	})
}

// A blockhash whose age is negative, after the clock stepped back, is stale.
func TestBuildFetchesAgainAfterTheClockStepsBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := &node{}
		var back time.Duration
		txs, _ := newTransactions(n, func() time.Time { return time.Now().Add(-back) })
		_, _, err := txs.Build(t.Context(), underpaid, payer)
		require.NoError(t, err)
		back = time.Minute
		tx, _, err := txs.Build(t.Context(), underpaid, payer)
		require.NoError(t, err)
		assert.Equal(t, paysTheRest(t, 2), tx)
	})
}

// A caller whose context ends stops waiting, but the fetch it started goes on, under a deadline
// of its own, for the callers still waiting.
func TestBuildLeavesTheFetchToTheCallersStillWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := &node{}
		txs, _ := newTransactions(n, time.Now)
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()
		var wg sync.WaitGroup
		wg.Go(func() {
			_, _, err := txs.Build(ctx, underpaid, payer)
			assert.ErrorIs(t, err, context.DeadlineExceeded)
		})
		synctest.Wait()

		tx, _, err := txs.Build(t.Context(), underpaid, payer)
		require.NoError(t, err)
		assert.Equal(t, paysTheRest(t, 1), tx)
		wg.Wait()
		assert.Equal(t, 1, n.calls())
		assert.Equal(t, []time.Duration{10 * time.Second}, n.deadlines, "the fetch's own 10 s, not its first caller's")
	})
}
