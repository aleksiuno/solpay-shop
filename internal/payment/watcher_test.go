package payment_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
	"github.com/aleksiuno/solpay-shop/internal/solana"
	"github.com/aleksiuno/solpay-shop/internal/solana/solanatest"
)

var (
	_ payment.Chain = (*solana.Client)(nil)
	_ payment.Chain = (*solanatest.Fake)(nil)

	// placed is when the tests' orders are placed. Their deadline is due, 15 minutes later.
	placed  = time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	due     = placed.Add(15 * time.Minute)
	cfg     = payment.DefaultConfig(5 * time.Second)
	errBoom = errors.New("boom")
)

// rig is a watcher over a fake node and an in-memory store, with a clock that a tick sets.
type rig struct {
	t     *testing.T
	chain *solanatest.Fake
	store *memStore
	now   time.Time
	w     *payment.Watcher
}

func newRig(t *testing.T, c payment.Config) *rig {
	r := &rig{t: t, chain: solanatest.NewFake(), store: newMemStore()}
	rec := audit.NewRecorder(r.store, slog.New(slog.DiscardHandler))
	r.w = payment.NewWatcher(r.chain, r.store, rec, merchant, c, func() time.Time { return r.now })
	return r
}

// order stores a pending order of 0.01 SOL, placed at placed, with a reference of its own.
func (r *rig) order() order.Order {
	id := uuid.NewV4()
	o := order.Order{
		ID: id, Quantity: 1, AmountLamports: 10_000_000, Reference: solanatest.Key(id.String()),
		Status: order.Pending, ExpiresAt: due, CreatedAt: placed,
	}
	r.store.add(o)
	return o
}

// tick runs one tick at the time at, requires it to succeed, and returns how many orders it
// polled.
func (r *rig) tick(at time.Time) int {
	r.t.Helper()
	r.now = at
	n, err := r.w.RunOnce(r.t.Context())
	require.NoError(r.t, err)
	return n
}

// paying returns a transaction, with signature sig and the block time at, that pays lamports to
// the merchant and lists o's reference.
func paying(o order.Order, sig string, lamports int64, at time.Time) solana.Tx {
	return solana.Tx{
		Signature:    sig,
		Slot:         at.Unix(),
		BlockTime:    &at,
		Keys:         []string{payer, merchant, solana.SystemProgram, o.Reference},
		Pre:          []int64{10_000_000_000, 0, 1, 0},
		Post:         []int64{10_000_000_000 - fee - lamports, lamports, 1, 0},
		Instructions: []solana.Instruction{transfer(merchantIdx, uint64(lamports), referenceIdx)},
	}
}

func TestDefaultConfig(t *testing.T) {
	assert.Equal(t, payment.Config{
		PollInterval: 7 * time.Second, Batch: 8, SignatureLimit: 100, LateWindow: time.Hour, RPCTimeout: 10 * time.Second,
	}, payment.DefaultConfig(7*time.Second))
}

func TestWatcherCreditsAPayment(t *testing.T) {
	r := newRig(t, cfg)
	o := r.order()
	at := placed.Add(time.Minute)
	r.chain.Add(o.Reference, paying(o, "sig-1", 10_000_000, at))

	assert.Equal(t, 1, r.tick(placed.Add(2*time.Minute)))
	got := r.store.get(o.ID)
	assert.Equal(t, order.Paid, got.Status)
	assert.Equal(t, int64(10_000_000), got.ReceivedLamports)
	assert.Equal(t, &at, got.PaidAt)
	assert.Equal(t, []audit.Kind{audit.PaymentSeen, audit.PaymentVerified}, r.store.kinds(o.ID))
	assert.Equal(t, payment.Payment{
		Signature: "sig-1", OrderID: o.ID, Outcome: payment.Credited, Lamports: 10_000_000, Slot: at.Unix(), BlockTime: &at,
	}, r.store.ledger["sig-1"])
}

func TestWatcherCreditsAnOverpayment(t *testing.T) {
	r := newRig(t, cfg)
	o := r.order()
	r.chain.Add(o.Reference, paying(o, "sig-1", 12_500_000, placed.Add(time.Minute)))

	r.tick(placed.Add(2 * time.Minute))
	got := r.store.get(o.ID)
	assert.Equal(t, order.Paid, got.Status)
	assert.Equal(t, int64(12_500_000), got.ReceivedLamports)
	assert.Equal(t, []audit.Kind{audit.PaymentSeen, audit.PaymentVerified, audit.PaymentOverpaid}, r.store.kinds(o.ID))
	assert.Equal(t, map[string]any{"surplus_lamports": int64(2_500_000)}, r.store.events[2].Data)
}

// A transaction the ledger has is not fetched or applied again, however often the node lists it.
func TestWatcherAppliesATransactionOnce(t *testing.T) {
	r := newRig(t, cfg)
	o := r.order()
	r.chain.Add(o.Reference, paying(o, "sig-1", 4_000_000, placed.Add(time.Minute)))

	for i := range 3 {
		assert.Equal(t, 1, r.tick(placed.Add(2*time.Minute+time.Duration(i)*cfg.PollInterval)))
	}
	assert.Equal(t, 3, r.chain.Calls(solanatest.GetSignaturesForAddress))
	assert.Equal(t, 1, r.chain.Calls(solanatest.GetTransaction))
	assert.Equal(t, []audit.Kind{audit.PaymentSeen, audit.PaymentUnderpaid}, r.store.kinds(o.ID))
	assert.Len(t, r.store.ledger, 1)
	assert.Equal(t, int64(4_000_000), r.store.get(o.ID).ReceivedLamports)
}

func TestWatcherCreditsATopUp(t *testing.T) {
	r := newRig(t, cfg)
	o := r.order()
	r.chain.Add(o.Reference, paying(o, "sig-1", 4_000_000, placed.Add(time.Minute)))
	r.tick(placed.Add(2 * time.Minute))
	require.Equal(t, order.Underpaid, r.store.get(o.ID).Status)

	at := placed.Add(3 * time.Minute)
	r.chain.Add(o.Reference, paying(o, "sig-2", 6_000_000, at))
	r.tick(placed.Add(4 * time.Minute))
	got := r.store.get(o.ID)
	assert.Equal(t, order.Paid, got.Status)
	assert.Equal(t, int64(10_000_000), got.ReceivedLamports)
	assert.Equal(t, &at, got.PaidAt)
	assert.Equal(t, []audit.Kind{audit.PaymentSeen, audit.PaymentUnderpaid, audit.PaymentSeen, audit.PaymentVerified}, r.store.kinds(o.ID))
}

// The node lists transactions newest first; they are applied oldest first, so the one that
// completes the amount is the one that pays the order.
func TestWatcherAppliesTheOldestFirst(t *testing.T) {
	r := newRig(t, cfg)
	o := r.order()
	r.chain.Add(o.Reference, paying(o, "sig-1", 4_000_000, placed.Add(time.Minute)))
	second := placed.Add(2 * time.Minute)
	r.chain.Add(o.Reference, paying(o, "sig-2", 6_000_000, second))

	r.tick(placed.Add(3 * time.Minute))
	got := r.store.get(o.ID)
	assert.Equal(t, order.Paid, got.Status)
	assert.Equal(t, &second, got.PaidAt)
}

func TestWatcherReadsUpToTheSignatureLimit(t *testing.T) {
	c := cfg
	c.SignatureLimit = 1
	r := newRig(t, c)
	o := r.order()
	r.chain.Add(o.Reference, paying(o, "sig-1", 4_000_000, placed.Add(time.Minute)))
	r.chain.Add(o.Reference, paying(o, "sig-2", 3_000_000, placed.Add(2*time.Minute)))

	r.tick(placed.Add(3 * time.Minute))
	assert.Len(t, r.store.ledger, 1)
	assert.Contains(t, r.store.ledger, "sig-2", "the newest")
}

// A payment made before the deadline counts when the node lists it only after the deadline.
func TestWatcherCreditsAnOnTimePaymentListedLate(t *testing.T) {
	r := newRig(t, cfg)
	o := r.order()
	r.chain.Add(o.Reference, paying(o, "sig-1", 10_000_000, due.Add(-time.Second)))

	r.tick(due.Add(30 * time.Second))
	assert.Equal(t, order.Paid, r.store.get(o.ID).Status)
	assert.Equal(t, []audit.Kind{audit.PaymentSeen, audit.PaymentVerified}, r.store.kinds(o.ID))
}

// A transaction that the node gives no block time for is timed by the poll that finds it.
func TestWatcherTimesAPaymentWithoutABlockTimeByThePoll(t *testing.T) {
	r := newRig(t, cfg)
	o := r.order()
	tx := paying(o, "sig-1", 10_000_000, placed)
	tx.BlockTime = nil
	r.chain.Add(o.Reference, tx)

	now := placed.Add(time.Minute)
	r.tick(now)
	assert.Equal(t, &now, r.store.get(o.ID).PaidAt)
}

// An underpaid order is still polled after its deadline, within the late window, and a payment
// found then is recorded as late and credits nothing.
func TestWatcherRecordsALatePayment(t *testing.T) {
	r := newRig(t, cfg)
	o := r.order()
	r.chain.Add(o.Reference, paying(o, "sig-1", 4_000_000, placed.Add(time.Minute)))
	r.tick(placed.Add(2 * time.Minute))
	r.chain.Add(o.Reference, paying(o, "sig-2", 6_000_000, due.Add(time.Minute)))

	assert.Equal(t, 1, r.tick(due.Add(cfg.LateWindow-time.Microsecond)), "the window's last instant")
	got := r.store.get(o.ID)
	assert.Equal(t, order.Underpaid, got.Status)
	assert.Equal(t, int64(4_000_000), got.ReceivedLamports)
	assert.Equal(t, payment.Late, r.store.ledger["sig-2"].Outcome)
	assert.Zero(t, r.tick(due.Add(cfg.LateWindow+cfg.PollInterval)), "the window is over")
}

// With more orders due than a tick takes, the next ticks take the rest before any order comes
// round again.
func TestWatcherPollsEveryDueOrderInTurn(t *testing.T) {
	r := newRig(t, cfg)
	var orders []order.Order
	for range 20 {
		orders = append(orders, r.order())
	}
	assert.Equal(t, 8, r.tick(placed.Add(time.Second)))
	assert.Equal(t, 8, r.tick(placed.Add(2*time.Second)))
	assert.Equal(t, 4, r.tick(placed.Add(3*time.Second)))
	for _, o := range orders {
		assert.True(t, r.store.get(o.ID).next.After(placed), "every order was polled")
	}
	assert.Equal(t, 20, r.chain.Calls(solanatest.GetSignaturesForAddress))
}

// A failed call to the node is recorded and ends the tick with its error. The order whose poll
// failed still gets its next poll time, and the orders after it wait for the next tick.
func TestWatcherEndsTheTickOnAFailedCall(t *testing.T) {
	limited := fmt.Errorf("%w: HTTP 429", solana.ErrRateLimited)
	tests := []struct {
		method string
		err    error
		sig    string
		data   map[string]any
	}{
		{solanatest.GetSignaturesForAddress, limited, "", map[string]any{
			"method": "getSignaturesForAddress", "error": "solana: rate limited: HTTP 429", "status": 429,
		}},
		{solanatest.GetTransaction, errBoom, "sig-1", map[string]any{"method": "getTransaction", "error": "boom"}},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			r := newRig(t, cfg)
			first, second := r.order(), r.order()
			r.store.find(second.ID).next = new(placed.Add(time.Second)) // due after first
			r.chain.Add(first.Reference, paying(first, "sig-1", 10_000_000, placed))
			r.chain.FailNext(tt.method, tt.err)
			now := placed.Add(time.Minute)
			r.now = now

			n, err := r.w.RunOnce(t.Context())
			require.ErrorIs(t, err, tt.err)
			assert.Equal(t, 1, n)
			assert.Empty(t, r.store.ledger)
			assert.Equal(t, []audit.Event{{
				OrderID: &first.ID, Kind: audit.RPCError, Message: "RPC call failed", Signature: tt.sig, Data: tt.data,
			}}, r.store.events)
			assert.Equal(t, new(now.Add(cfg.PollInterval)), r.store.get(first.ID).next)

			assert.Equal(t, 1, r.tick(now), "only the order that was not polled is due")
			assert.Equal(t, new(now.Add(cfg.PollInterval)), r.store.get(second.ID).next)
		})
	}
}

// Each call that the node does not answer gives up after RPCTimeout, is recorded, and ends the
// tick.
func TestWatcherTimesOutACall(t *testing.T) {
	c := cfg
	c.RPCTimeout = 3 * time.Second
	for _, method := range []string{solanatest.GetGenesisHash, solanatest.GetSignaturesForAddress, solanatest.GetTransaction} {
		t.Run(method, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newRig(t, c)
				o := r.order()
				r.order()
				r.chain.Add(o.Reference, paying(o, "sig-1", 10_000_000, placed))
				r.chain.BlockUntilDone(method)
				r.now = placed.Add(time.Minute)
				want := audit.Event{
					OrderID: &o.ID, Kind: audit.RPCTimeout, Message: "RPC call timed out",
					Data: map[string]any{"method": method, "timeout": "3s"},
				}
				wantN := 1
				switch method {
				case solanatest.GetGenesisHash:
					want.OrderID, wantN = nil, 0
				case solanatest.GetTransaction:
					want.Signature = "sig-1"
				}

				start := time.Now()
				n, err := r.w.RunOnce(t.Context())
				require.ErrorIs(t, err, context.DeadlineExceeded)
				assert.Equal(t, c.RPCTimeout, time.Since(start))
				assert.Equal(t, wantN, n)
				assert.Equal(t, []audit.Event{want}, r.store.events)
			})
		})
	}
}

// When an order's poll and its rescheduling both fail, each failure is recorded as what it is.
func TestWatcherRecordsBothFailuresOfAnOrder(t *testing.T) {
	r := newRig(t, cfg)
	o := r.order()
	r.chain.FailNext(solanatest.GetSignaturesForAddress, fmt.Errorf("%w: HTTP 429", solana.ErrRateLimited))
	r.store.fail["MarkPolled"] = fmt.Errorf("mark polled: %w", context.DeadlineExceeded)
	r.now = placed

	_, err := r.w.RunOnce(t.Context())
	require.ErrorIs(t, err, solana.ErrRateLimited)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, []audit.Kind{audit.RPCError, audit.WatcherError}, r.store.kinds(o.ID))
	assert.Equal(t, 429, r.store.events[0].Data["status"])
}

// A permanent failure to list one order's transactions is recorded, and the tick goes on to the
// next order.
func TestWatcherGoesOnAfterAPermanentFailure(t *testing.T) {
	r := newRig(t, cfg)
	first, second := r.order(), r.order()
	r.store.find(second.ID).next = new(placed.Add(time.Second))
	r.chain.Add(second.Reference, paying(second, "sig-2", 10_000_000, placed))
	r.chain.FailNext(solanatest.GetSignaturesForAddress, fmt.Errorf("%w: signature entry 0 is null", solana.ErrPermanent))

	assert.Equal(t, 2, r.tick(placed.Add(time.Minute)))
	assert.Equal(t, []audit.Kind{audit.RPCError}, r.store.kinds(first.ID))
	assert.Equal(t, order.Paid, r.store.get(second.ID).Status)
}

// A failure of the shop's own in one order's poll is recorded, and the tick goes on to the next
// order.
func TestWatcherGoesOnAfterAStoreFailure(t *testing.T) {
	tests := []struct {
		method string
		status order.Status // the failing order's, after the tick
	}{
		{"KnownSignatures", order.Pending},
		{"Apply", order.Pending},
		{"MarkPolled", order.Paid},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			r := newRig(t, cfg)
			first, second := r.order(), r.order()
			r.store.find(second.ID).next = new(placed.Add(time.Second))
			r.chain.Add(first.Reference, paying(first, "sig-1", 10_000_000, placed))
			r.chain.Add(second.Reference, paying(second, "sig-2", 10_000_000, placed))
			r.store.fail[tt.method] = errBoom

			assert.Equal(t, 2, r.tick(placed.Add(time.Minute)))
			assert.Equal(t, tt.status, r.store.get(first.ID).Status)
			assert.Equal(t, order.Paid, r.store.get(second.ID).Status)
			assert.Equal(t, []audit.Event{{
				OrderID: &first.ID, Kind: audit.WatcherError, Message: "watcher failed",
				Data: map[string]any{"error": "poll order " + first.ID.String() + ": boom"},
			}}, r.store.ofKind(audit.WatcherError))
		})
	}
}

func TestWatcherEndsTheTickWhenItCannotFindTheOrders(t *testing.T) {
	r := newRig(t, cfg)
	r.order()
	r.store.fail["WatchSet"] = errBoom

	n, err := r.w.RunOnce(t.Context())
	require.ErrorIs(t, err, errBoom)
	assert.Zero(t, n)
	assert.Equal(t, []audit.Event{{
		Kind: audit.WatcherError, Message: "watcher failed", Data: map[string]any{"error": "boom"},
	}}, r.store.events)
}

// Once ctx has ended, a failure ends the tick, whatever failed, and nothing is recorded.
func TestWatcherRecordsNothingOnceCanceled(t *testing.T) {
	for name, fail := range map[string]func(r *rig){
		"node":  func(r *rig) { r.chain.FailNext(solanatest.GetSignaturesForAddress, errBoom) },
		"store": func(r *rig) { r.store.fail["Apply"] = errBoom },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, cfg)
			o := r.order()
			r.order()
			r.chain.Add(o.Reference, paying(o, "sig-1", 10_000_000, placed))
			fail(r)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			r.now = placed.Add(time.Minute)

			n, err := r.w.RunOnce(ctx)
			require.ErrorIs(t, err, errBoom)
			assert.Equal(t, 1, n)
			assert.Empty(t, r.store.events)
		})
	}
}

// Nothing is polled until the node is found to serve devnet. A failed check is recorded and
// retried on the next tick; once it passes, it is not made again.
func TestWatcherChecksTheClusterFirst(t *testing.T) {
	r := newRig(t, cfg)
	r.order()
	r.chain.FailNext(solanatest.GetGenesisHash, errBoom)

	n, err := r.w.RunOnce(t.Context())
	require.ErrorIs(t, err, errBoom)
	assert.Zero(t, n)
	assert.Zero(t, r.chain.Calls(solanatest.GetSignaturesForAddress))
	assert.Equal(t, []audit.Event{{
		Kind: audit.RPCError, Message: "RPC call failed", Data: map[string]any{"method": "getGenesisHash", "error": "boom"},
	}}, r.store.events)

	assert.Equal(t, 1, r.tick(placed.Add(time.Minute)))
	assert.Equal(t, 1, r.tick(placed.Add(2*time.Minute)))
	assert.Equal(t, 2, r.chain.Calls(solanatest.GetGenesisHash))
}

// A node that serves another cluster stops the tick before any poll. The error is for the caller
// to act on, so nothing is recorded.
func TestWatcherPollsNothingOnAnotherCluster(t *testing.T) {
	r := newRig(t, cfg)
	r.order()
	r.chain.FailNext(solanatest.GetGenesisHash, fmt.Errorf("%w: genesis hash x", solana.ErrNotDevnet))

	n, err := r.w.RunOnce(t.Context())
	require.ErrorIs(t, err, solana.ErrNotDevnet)
	assert.Zero(t, n)
	assert.Zero(t, r.chain.Calls(solanatest.GetSignaturesForAddress))
	assert.Empty(t, r.store.events)
}

func TestWatcherRejectsAFailedTransactionWithoutFetchingIt(t *testing.T) {
	r := newRig(t, cfg)
	o := r.order()
	tx := paying(o, "sig-1", 10_000_000, placed.Add(time.Minute))
	tx.Failed = true
	r.chain.Add(o.Reference, tx)

	r.tick(placed.Add(2 * time.Minute))
	assert.Zero(t, r.chain.Calls(solanatest.GetTransaction))
	assert.Equal(t, payment.TxFailed, r.store.ledger["sig-1"].Reason)
	assert.Equal(t, order.Pending, r.store.get(o.ID).Status)
	assert.Equal(t, []audit.Kind{audit.PaymentSeen, audit.PaymentInvalidTx}, r.store.kinds(o.ID))
}

// A transaction the node can never return is recorded as unfetchable, and the tick goes on.
func TestWatcherRejectsAnUnfetchableTransaction(t *testing.T) {
	r := newRig(t, cfg)
	first, second := r.order(), r.order()
	r.store.find(second.ID).next = new(placed.Add(time.Second))
	r.chain.Add(first.Reference, paying(first, "sig-1", 10_000_000, placed))
	r.chain.Add(second.Reference, paying(second, "sig-2", 10_000_000, placed))
	r.chain.FailNext(solanatest.GetTransaction, fmt.Errorf("%w: unsupported version", solana.ErrPermanent))

	assert.Equal(t, 2, r.tick(placed.Add(time.Minute)))
	assert.Equal(t, payment.Unfetchable, r.store.ledger["sig-1"].Reason)
	assert.Equal(t, order.Pending, r.store.get(first.ID).Status)
	require.Equal(t, []audit.Kind{audit.PaymentSeen, audit.PaymentInvalidTx}, r.store.kinds(first.ID))
	assert.Equal(t, map[string]any{"reason": "unfetchable", "error": "solana: permanent failure: unsupported version"}, r.store.events[1].Data)
	assert.Equal(t, order.Paid, r.store.get(second.ID).Status)
}

// A transaction the node lists but cannot return yet is left for the next poll, and the poll goes
// on to the others; it counts once the node returns it.
func TestWatcherWaitsForAListedTransaction(t *testing.T) {
	r := newRig(t, cfg)
	o := r.order()
	r.chain.Add(o.Reference, paying(o, "sig-1", 4_000_000, placed))
	r.chain.Add(o.Reference, paying(o, "sig-2", 6_000_000, placed.Add(time.Minute)))
	r.chain.NotFound("sig-1", 1)

	now := placed.Add(2 * time.Minute)
	r.tick(now)
	assert.Equal(t, order.Underpaid, r.store.get(o.ID).Status)
	assert.NotContains(t, r.store.ledger, "sig-1")
	assert.Empty(t, r.store.ofKind(audit.RPCError))

	r.tick(now.Add(cfg.PollInterval))
	assert.Equal(t, order.Paid, r.store.get(o.ID).Status)
	assert.Equal(t, 3, r.chain.Calls(solanatest.GetTransaction))
}
