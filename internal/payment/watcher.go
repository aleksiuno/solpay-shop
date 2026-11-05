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
}

// Config sets how the watcher polls. DefaultConfig gives the shop's settings.
type Config struct {
	// PollInterval is how often an order is polled.
	PollInterval time.Duration
	// Batch is the most orders one tick polls.
	Batch int
	// SignatureLimit is the most transactions read for one reference in one poll.
	SignatureLimit int
	// LateWindow is how long after its deadline an underpaid or expired order is still polled, so
	// that a late payment is seen and the order can be refunded.
	LateWindow time.Duration
	// RPCTimeout bounds each call to the node, the wait for its rate limiter included.
	RPCTimeout time.Duration
}

// DefaultConfig returns the shop's watcher settings, polling every pollInterval.
func DefaultConfig(pollInterval time.Duration) Config {
	return Config{
		PollInterval:   pollInterval,
		Batch:          8,
		SignatureLimit: 100,
		LateWindow:     time.Hour,
		RPCTimeout:     10 * time.Second,
	}
}

// Watcher finds the payments for orders by asking the RPC node for the transactions that list each
// order's reference. RunOnce is one round of it. A Watcher is not safe for concurrent use.
type Watcher struct {
	chain    Chain
	store    Store
	rec      *audit.Recorder
	merchant string
	cfg      Config
	now      func() time.Time
	devnet   bool // the node has been found to serve devnet
}

// NewWatcher returns a Watcher that reads chain, keeps its state in store, records what goes
// wrong with rec and looks for payments to merchant. now is its clock.
func NewWatcher(chain Chain, store Store, rec *audit.Recorder, merchant string, cfg Config, now func() time.Time) *Watcher {
	return &Watcher{chain: chain, store: store, rec: rec, merchant: merchant, cfg: cfg, now: now}
}

// RunOnce polls the orders that are due, at most cfg.Batch of them and the longest due first, and
// returns how many it polled. Until the node has once been found to serve devnet, RunOnce checks
// that first and polls nothing unless it does. A node that serves another cluster gives
// solana.ErrNotDevnet, which is returned without being recorded.
//
// Each order it polls is next due PollInterval later, even when the poll fails, so an order whose
// poll keeps failing waits behind the others instead of holding up the batch.
//
// A failed call to the node is recorded as rpc.timeout if it ran out of time, or else as rpc.error,
// and ends the tick with its error: the next call would most likely fail too, so the caller should
// back off. A permanent failure (solana.ErrPermanent) belongs to one order, so it is recorded as
// rpc.error and the tick goes on to the next order. Any other failure is the shop's own, such as
// a database error, and is recorded as watcher.error: in an order's poll, the tick goes on; in
// finding the due orders, it ends. Once ctx has ended, a failure ends the tick and is not
// recorded. Transactions that the node lists but cannot return are dealt with in the poll.
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
	orders, err := w.store.WatchSet(ctx, now, now.Add(-w.cfg.LateWindow), w.cfg.Batch)
	if err != nil {
		w.fail(ctx, nil, err)
		return 0, err
	}
	for i, o := range orders {
		errs := []error{w.poll(ctx, o, now), w.store.MarkPolled(ctx, o.ID, new(now.Add(w.cfg.PollInterval)))}
		end := false
		for _, err := range errs {
			if err != nil {
				end = w.fail(ctx, &o.ID, fmt.Errorf("poll order %s: %w", o.ID, err)) || end
			}
		}
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

// poll asks the node for the transactions that list o's reference, and applies each one that the
// ledger does not have yet, oldest first. One that the node lists but cannot return yet is left for
// the next poll.
func (w *Watcher) poll(ctx context.Context, o order.Order, now time.Time) error {
	var listed []solana.SignatureInfo
	err := w.rpc(ctx, getSignaturesForAddress, "", func(ctx context.Context) (err error) {
		listed, err = w.chain.Signatures(ctx, o.Reference, w.cfg.SignatureLimit)
		return err
	})
	if err != nil {
		return err
	}
	sigs := make([]string, len(listed))
	for i, s := range listed {
		sigs[i] = s.Signature
	}
	known, err := w.store.KnownSignatures(ctx, sigs)
	if err != nil {
		return err
	}
	for _, s := range slices.Backward(listed) {
		if known[s.Signature] {
			continue
		}
		c, err := w.candidate(ctx, o, s)
		if errors.Is(err, solana.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := w.store.Apply(ctx, o.ID, func(cur order.Order) (Decision, error) { return Decide(cur, c, now) }); err != nil {
			return err
		}
	}
	return nil
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
