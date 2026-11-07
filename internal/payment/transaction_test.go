package payment_test

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
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
// one, or down for every one.
type node struct {
	mu        sync.Mutex
	fail      error
	down      error
	deadlines []time.Duration // how long each call's context had before its deadline
}

func (n *node) LatestBlockhash(ctx context.Context) (string, error) {
	d, _ := ctx.Deadline()
	n.mu.Lock()
	n.deadlines = append(n.deadlines, time.Until(d))
	call, err := len(n.deadlines), cmp.Or(n.fail, n.down)
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

// A failed fetch is not kept: the next caller waits for a new one, which starts 2.5 s after the
// failed one began. The failure is logged.
func TestBuildFetchesAgainAfterAFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := &node{fail: errBoom}
		txs, buf := newTransactions(n, time.Now)
		start := time.Now()
		_, _, err := txs.Build(t.Context(), underpaid, payer)
		var e *payment.NoBlockhashError
		require.ErrorAs(t, err, &e)
		require.ErrorIs(t, err, errBoom)
		assert.Equal(t, 1500*time.Millisecond, e.RetryAfter, "until 2.5 s after the fetch began")
		assert.Contains(t, buf.String(),
			`"level":"ERROR","msg":"no blockhash from the RPC node","error":"boom","retry_after":1500000000}`)

		tx, _, err := txs.Build(t.Context(), underpaid, payer)
		require.NoError(t, err)
		assert.Equal(t, paysTheRest(t, 2), tx)
		assert.Equal(t, 3500*time.Millisecond, time.Since(start), "from a fetch at 2.5 s")
		assert.Equal(t, 2, n.calls())
	})
}

// While the node fails, however many callers ask, fetches start 2.5 s apart; once the node has
// rate-limited one, none starts for 15 s, and callers get its error at once. Each failed fetch is
// logged once.
func TestBuildPacesTheFetchesWhileTheNodeFails(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		calls   int           // node calls
		retry   time.Duration // the longest a caller is told to wait
		answers int           // errors to the callers
	}{
		// Fetches at 0, 2.5, 5 ... 17.5 s, each failing all 50 callers 1 s later.
		{"refused", errBoom, 8, 1500 * time.Millisecond, 8 * 50},
		// Fetches at 0 and 16 s, and an answer at once every 100 ms after the first one fails.
		{"rate limited", fmt.Errorf("%w: HTTP 429", solana.ErrRateLimited), 2, 15 * time.Second, 166 * 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				n := &node{down: tt.err}
				txs, buf := newTransactions(n, time.Now)
				// 50 callers ask every 100 ms until 18.55 s, after the last answer and before another ask.
				ctx, cancel := context.WithTimeout(t.Context(), 18550*time.Millisecond)
				defer cancel()
				var mu sync.Mutex
				var retries []time.Duration
				var wg sync.WaitGroup
				for range 50 {
					wg.Go(func() {
						for ctx.Err() == nil {
							var e *payment.NoBlockhashError
							if _, _, err := txs.Build(ctx, underpaid, payer); errors.As(err, &e) {
								mu.Lock()
								retries = append(retries, e.RetryAfter)
								mu.Unlock()
							}
							time.Sleep(100 * time.Millisecond)
						}
					})
				}
				wg.Wait()
				assert.Equal(t, tt.calls, n.calls())
				assert.Equal(t, tt.retry, slices.Max(retries))
				assert.Len(t, retries, tt.answers)
				assert.Equal(t, tt.calls, strings.Count(buf.String(), `"msg":"no blockhash from the RPC node"`))
			})
		})
	}
}

// After the clock stepped back, a blockhash whose age is below zero is stale, and so is the pause
// after a failure.
func TestBuildFetchesAgainAfterTheClockStepsBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := &node{}
		var back time.Duration
		txs, _ := newTransactions(n, func() time.Time { return time.Now().Add(-back) })
		start := time.Now()
		_, _, err := txs.Build(t.Context(), underpaid, payer)
		require.NoError(t, err)
		back, n.fail = time.Minute, errBoom
		_, _, err = txs.Build(t.Context(), underpaid, payer)
		require.ErrorIs(t, err, errBoom, "a fetch, not the blockhash fetched 1 s before")
		back = 2 * time.Minute
		tx, _, err := txs.Build(t.Context(), underpaid, payer)
		require.NoError(t, err)
		assert.Equal(t, paysTheRest(t, 3), tx)
		assert.Equal(t, 3*time.Second, time.Since(start), "no pause after the failure")
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
