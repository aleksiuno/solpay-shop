package payment

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/solana"
)

// Chain is the Solana RPC node, as the watcher reads it. *solana.Client is one.
type Chain interface {
	// CheckDevnet returns solana.ErrNotDevnet unless the node serves devnet.
	CheckDevnet(ctx context.Context) error
	// Signatures lists up to limit transactions that mention reference, newest first.
	Signatures(ctx context.Context, reference string, limit int) ([]solana.SignatureInfo, error)
	// Transaction fetches a transaction. It returns solana.ErrNotFound while the node has not
	// indexed it, and solana.ErrPermanent when asking again cannot help.
	Transaction(ctx context.Context, signature string) (solana.Tx, error)
}

// The RPC methods behind the calls of Chain, as events name them.
const (
	getGenesisHash          = "getGenesisHash"
	getSignaturesForAddress = "getSignaturesForAddress"
	getTransaction          = "getTransaction"
)

// Store keeps the orders that the watcher polls and the payment ledger. *db.Payments is one.
type Store interface {
	// WatchSet returns up to limit orders that are due for a poll at now, the longest due first:
	// the pending orders, and the underpaid and expired ones whose deadline is after lateCutoff.
	WatchSet(ctx context.Context, now, lateCutoff time.Time, limit int) ([]order.Order, error)
	// MarkPolled sets when the order is next due for a poll. A nil next means never again.
	MarkPolled(ctx context.Context, orderID uuid.UUID, next *time.Time) error
	// KnownSignatures returns the set of those of sigs that the ledger already has.
	KnownSignatures(ctx context.Context, sigs []string) (map[string]bool, error)
	// Apply passes the order, read under a lock, to decide, and stores the decision with its
	// ledger row unless the ledger already has the signature. It reports whether it stored it.
	Apply(ctx context.Context, orderID uuid.UUID, decide func(order.Order) (Decision, error)) (bool, error)
	// Expire moves the order from pending to expired if its deadline is before cutoff, with an
	// order.expired event that names the unresolved signatures, and reports whether it did. Any
	// other order is left as it is.
	Expire(ctx context.Context, orderID uuid.UUID, cutoff time.Time, unresolved []string) (bool, error)
	// StopPolling ends the polling of every underpaid or expired order whose deadline is at or
	// before lateCutoff, and returns how many orders it changed.
	StopPolling(ctx context.Context, lateCutoff time.Time) (int64, error)
}

// Config sets how the watcher polls. DefaultConfig gives the shop's settings.
type Config struct {
	// PollInterval is how often an order is polled while it is pending or before its deadline.
	PollInterval time.Duration
	// Batch is the most orders one tick polls.
	Batch int
	// SignatureLimit is the most transactions read for one reference in one poll.
	SignatureLimit int
	// LateWindow is how long after its deadline an underpaid or expired order is still polled, so
	// that a late payment is seen and the order can be refunded.
	LateWindow time.Duration
	// ExpiryGrace is how long after its deadline a pending order is still polled before it is
	// expired, so that a payment made just before the deadline, which the node lists a little
	// later, still pays it.
	ExpiryGrace time.Duration
	// LateCadence is how often an order is polled after its deadline once it is no longer pending.
	LateCadence time.Duration
	// RPCTimeout bounds each call to the node, the wait for its rate limiter included.
	RPCTimeout time.Duration
	// MaxBackoff is the longest Run waits after failed ticks.
	MaxBackoff time.Duration
	// RateLimitBackoff is the least Run waits after a tick that the node rate-limited.
	RateLimitBackoff time.Duration
}

// DefaultConfig returns the shop's watcher settings, polling every pollInterval.
func DefaultConfig(pollInterval time.Duration) Config {
	return Config{
		PollInterval:     pollInterval,
		Batch:            8,
		SignatureLimit:   100,
		LateWindow:       time.Hour,
		ExpiryGrace:      2 * time.Minute,
		LateCadence:      time.Minute,
		RPCTimeout:       10 * time.Second,
		MaxBackoff:       time.Minute,
		RateLimitBackoff: 15 * time.Second,
	}
}

// Watcher finds the payments for orders by asking the RPC node for the transactions that list each
// order's reference. Run polls until it is stopped, and RunOnce is one round of it. A Watcher is
// not safe for concurrent use.
type Watcher struct {
	chain    Chain
	store    Store
	rec      *audit.Recorder
	merchant string
	cfg      Config
	now      func() time.Time
	devnet   bool      // the node has been found to serve devnet
	tickAt   time.Time // by now, when the last tick to poll began polling
}

// NewWatcher returns a Watcher that reads chain, keeps its state in store, records what goes
// wrong with rec and looks for payments to merchant. now is its clock.
func NewWatcher(chain Chain, store Store, rec *audit.Recorder, merchant string, cfg Config, now func() time.Time) *Watcher {
	return &Watcher{chain: chain, store: store, rec: rec, merchant: merchant, cfg: cfg, now: now}
}

// Run runs ticks of RunOnce until ctx ends, and then returns nil. It records watcher.started when
// it begins and watcher.stopped when it returns, the latter even though ctx has ended.
//
// A tick starts only when the one before has ended. After a tick that polled a full batch, the
// next starts at once, since more orders are likely due; the node's rate limiter paces the calls.
// After a shorter one, the next starts PollInterval after this one began polling, or at once if
// that time has passed: the orders it polled every PollInterval are due again then. After a
// failed tick, Run backs
// off: it waits PollInterval doubled for each failed tick in a row, up to MaxBackoff, and at least
// RateLimitBackoff when the node rate-limited the shop. The first tick that succeeds ends the
// backoff.
//
// Run returns early only with solana.ErrNotDevnet, when the node serves another cluster: the
// watcher cannot work, so the caller should stop. It returns that error even if ctx ends at the
// same time.
func (w *Watcher) Run(ctx context.Context) error {
	w.rec.Record(ctx, audit.Event{Kind: audit.WatcherStarted, Message: "watcher started", Data: map[string]any{
		"poll_interval": w.cfg.PollInterval.String(), "batch": w.cfg.Batch, "late_window": w.cfg.LateWindow.String(),
	}})
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		w.rec.Record(ctx, audit.Event{Kind: audit.WatcherStopped, Message: "watcher stopped"})
	}()
	timer := time.NewTimer(0)
	defer timer.Stop()
	failed := 0 // ticks in a row
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		n, err := w.RunOnce(ctx)
		switch {
		case errors.Is(err, solana.ErrNotDevnet):
			return err
		case ctx.Err() != nil:
			return nil
		case err != nil:
			failed++
			timer.Reset(w.backoff(failed, errors.Is(err, solana.ErrRateLimited)))
		default:
			failed = 0
			wait := w.tickAt.Add(w.cfg.PollInterval).Sub(w.now()) // by the clock nextPoll uses
			if n == w.cfg.Batch {
				wait = 0 // more orders are likely due
			}
			timer.Reset(max(wait, 0))
		}
	}
}

// backoff returns how long Run waits after failed ticks in a row. See Run.
func (w *Watcher) backoff(failed int, rateLimited bool) time.Duration {
	d := min(w.cfg.PollInterval<<min(failed, 6), w.cfg.MaxBackoff)
	if rateLimited {
		d = max(d, w.cfg.RateLimitBackoff)
	}
	return d
}

// RunOnce polls the orders that are due, at most cfg.Batch of them and the longest due first, and
// returns how many it polled. Until the node has once been found to serve devnet, RunOnce checks
// that first and polls nothing unless it does. A node that serves another cluster gives
// solana.ErrNotDevnet, which is returned without being recorded.
//
// A pending order is expired by its first poll more than ExpiryGrace after its deadline that
// succeeds and finds no transaction that the node lists but cannot return yet, since one of those
// may pay the order on time. Once the order's late window is over, such transactions no longer
// hold the expiry back, and its event names them; nor does a listing of the order's reference that
// fails for good (solana.ErrPermanent). Any other failed poll holds the expiry back until a poll
// succeeds.
//
// An order is next due PollInterval later if it was polled at or before its deadline, or was
// pending when the tick took it and the tick did not expire it; otherwise LateCadence later, until
// its late window ends, and then never. An order gets its next poll time even when its poll fails,
// so one whose poll keeps failing waits behind the others instead of holding up the batch. Each
// tick also ends the polling of every underpaid or expired order whose late window is over, which
// WatchSet no longer returns, in case its last poll did not run in time.
//
// A failed call to the node is recorded as rpc.timeout if it ran out of time, or else as rpc.error,
// and ends the tick with its error: the next call would most likely fail too, so the caller should
// back off. A permanent failure (solana.ErrPermanent) belongs to one order, so it is recorded as
// rpc.error and the tick goes on to the next order. Any other failure is the shop's own, such as
// a database error, and is recorded as watcher.error; the tick goes on, unless it could not find
// the due orders. Once ctx has ended, a failure ends the tick and is not recorded. Transactions
// that the node lists but cannot return are dealt with in the poll.
func (w *Watcher) RunOnce(ctx context.Context) (int, error) {
	if !w.devnet {
		if err := w.rpc(ctx, getGenesisHash, "", w.chain.CheckDevnet); err != nil {
			if !errors.Is(err, solana.ErrNotDevnet) {
				w.fail(ctx, nil, err)
			}
			return 0, err
		}
		w.devnet = true
	}
	now := w.now()
	w.tickAt = now
	lateCutoff := now.Add(-w.cfg.LateWindow)
	if _, err := w.store.StopPolling(ctx, lateCutoff); err != nil && w.fail(ctx, nil, err) {
		return 0, err
	}
	orders, err := w.store.WatchSet(ctx, now, lateCutoff, w.cfg.Batch)
	if err != nil {
		w.fail(ctx, nil, err)
		return 0, err
	}
	for i, o := range orders {
		var errs []error
		end := false
		record := func(err error) {
			if err != nil {
				errs = append(errs, err)
				end = w.fail(ctx, &o.ID, fmt.Errorf("poll order %s: %w", o.ID, err)) || end
			}
		}
		unresolved, pollErr := w.poll(ctx, o, now)
		record(pollErr)
		expired, err := w.expire(ctx, o, now, unresolved, pollErr)
		record(err)
		if expired {
			o.Status = order.Expired
		}
		record(w.store.MarkPolled(ctx, o.ID, w.nextPoll(o, now)))
		if end {
			return i + 1, fmt.Errorf("poll order %s: %w", o.ID, errors.Join(errs...))
		}
	}
	return len(orders), nil
}

// fail records err, one failure in the poll of the order with orderID, or in no order's if orderID
// is nil, and reports whether it ends the tick. See RunOnce.
func (w *Watcher) fail(ctx context.Context, orderID *uuid.UUID, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	ev := audit.Event{OrderID: orderID}
	re, ok := errors.AsType[*rpcError](err)
	switch {
	case !ok:
		ev.Kind, ev.Message, ev.Data = audit.WatcherError, "watcher failed", map[string]any{"error": err.Error()}
	case errors.Is(re.err, context.DeadlineExceeded):
		ev.Kind, ev.Message, ev.Data = audit.RPCTimeout, "RPC call timed out", map[string]any{
			"method": re.method, "timeout": w.cfg.RPCTimeout.String(),
		}
	default:
		ev.Kind, ev.Message, ev.Data = audit.RPCError, "RPC call failed", map[string]any{
			"method": re.method, "error": re.err.Error(),
		}
		if errors.Is(re.err, solana.ErrRateLimited) {
			ev.Data["status"] = 429 // HTTP 429 Too Many Requests, or a JSON-RPC error with that code
		}
	}
	if ok {
		ev.Signature = re.signature
	}
	w.rec.Record(ctx, ev)
	return ok && !errors.Is(re.err, solana.ErrPermanent)
}

// nextPoll returns when o, polled at now, is next due for a poll, or nil for never. See RunOnce.
func (w *Watcher) nextPoll(o order.Order, now time.Time) *time.Time {
	if o.Status == order.Pending || !now.After(o.ExpiresAt) {
		return new(now.Add(w.cfg.PollInterval))
	}
	next := now.Add(w.cfg.LateCadence)
	if !next.Before(o.ExpiresAt.Add(w.cfg.LateWindow)) {
		return nil // WatchSet would not return it then
	}
	return &next
}

// poll asks the node for the transactions that list o's reference, and applies each one that the
// ledger does not have yet, oldest first. It returns those that the node lists but cannot return
// yet, oldest first; they are left for the next poll.
func (w *Watcher) poll(ctx context.Context, o order.Order, now time.Time) ([]string, error) {
	var listed []solana.SignatureInfo
	err := w.rpc(ctx, getSignaturesForAddress, "", func(ctx context.Context) (err error) {
		listed, err = w.chain.Signatures(ctx, o.Reference, w.cfg.SignatureLimit)
		return err
	})
	if err != nil {
		return nil, err
	}
	sigs := make([]string, len(listed))
	for i, s := range listed {
		sigs[i] = s.Signature
	}
	known, err := w.store.KnownSignatures(ctx, sigs)
	if err != nil {
		return nil, err
	}
	var unresolved []string
	for _, s := range slices.Backward(listed) {
		if known[s.Signature] {
			continue
		}
		c, err := w.candidate(ctx, o, s)
		if errors.Is(err, solana.ErrNotFound) {
			unresolved = append(unresolved, s.Signature)
			continue
		}
		if err != nil {
			return nil, err
		}
		if _, err := w.store.Apply(ctx, o.ID, func(cur order.Order) (Decision, error) { return Decide(cur, c, now) }); err != nil {
			return nil, err
		}
	}
	return unresolved, nil
}

// expire expires o, polled at now, if it is due to be, and reports whether it did. unresolved and
// pollErr are what the poll returned. See RunOnce.
func (w *Watcher) expire(ctx context.Context, o order.Order, now time.Time, unresolved []string, pollErr error) (bool, error) {
	// o is the order as WatchSet read it. If the poll's payments moved it on, Expire leaves it alone.
	if o.Status != order.Pending || !now.After(o.ExpiresAt.Add(w.cfg.ExpiryGrace)) {
		return false, nil
	}
	permanent := errors.Is(pollErr, solana.ErrPermanent)
	if pollErr != nil && !permanent {
		return false, nil // what the poll did not read or apply may pay o on time
	}
	if (permanent || len(unresolved) > 0) && now.Before(o.ExpiresAt.Add(w.cfg.LateWindow)) {
		return false, nil
	}
	return w.store.Expire(ctx, o.ID, now.Add(-w.cfg.ExpiryGrace), unresolved)
}

// candidate returns the transaction s with Verify's verdict on it as a payment to o. A failed
// transaction is rejected without being fetched, and one that the node can never return is
// rejected as unfetchable. Any other error from the node is returned.
func (w *Watcher) candidate(ctx context.Context, o order.Order, s solana.SignatureInfo) (Candidate, error) {
	c := Candidate{Signature: s.Signature, Slot: s.Slot, BlockTime: s.BlockTime}
	if s.Failed {
		c.Verdict.Reason = TxFailed
		return c, nil
	}
	var tx solana.Tx
	err := w.rpc(ctx, getTransaction, s.Signature, func(ctx context.Context) (err error) {
		tx, err = w.chain.Transaction(ctx, s.Signature)
		return err
	})
	switch {
	case errors.Is(err, solana.ErrPermanent):
		c.Verdict.Reason, c.Err = Unfetchable, err.Error()
	case err != nil:
		return Candidate{}, err
	default:
		c.Verdict = Verify(tx, w.merchant, o.Reference)
	}
	return c, nil
}

// rpc makes call, one call to the node for method, about the transaction signature if it is set,
// with cfg.RPCTimeout as its deadline. The error it returns is an *rpcError.
func (w *Watcher) rpc(ctx context.Context, method, signature string, call func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, w.cfg.RPCTimeout)
	defer cancel()
	if err := call(ctx); err != nil {
		return &rpcError{method: method, signature: signature, err: err}
	}
	return nil
}

// rpcError is a failed call to the node, as opposed to a failure of the shop's own. Its message is
// the call's error.
type rpcError struct {
	method    string
	signature string
	err       error
}

func (e *rpcError) Error() string { return e.err.Error() }
func (e *rpcError) Unwrap() error { return e.err }
