package payment

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/solana"
)

// Chain is the Solana RPC node, as the watcher reads it. *solana.Client is one.
type Chain interface {
	// Signatures lists up to limit transactions that mention reference, newest first.
	Signatures(ctx context.Context, reference string, limit int) ([]solana.SignatureInfo, error)
	Transaction(ctx context.Context, signature string) (solana.Tx, error)
}

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
}

// DefaultConfig returns the shop's watcher settings, polling every pollInterval.
func DefaultConfig(pollInterval time.Duration) Config {
	return Config{
		PollInterval:   pollInterval,
		Batch:          8,
		SignatureLimit: 100,
		LateWindow:     time.Hour,
	}
}

// Watcher finds the payments for orders by asking the RPC node for the transactions that list each
// order's reference. RunOnce is one round of it.
type Watcher struct {
	chain    Chain
	store    Store
	merchant string
	cfg      Config
	now      func() time.Time
}

// NewWatcher returns a Watcher that reads chain, keeps its state in store and looks for payments to
// merchant. now is its clock.
func NewWatcher(chain Chain, store Store, merchant string, cfg Config, now func() time.Time) *Watcher {
	return &Watcher{chain: chain, store: store, merchant: merchant, cfg: cfg, now: now}
}

// RunOnce polls the orders that are due, at most cfg.Batch of them and the longest due first, and
// returns how many it polled.
//
// Each order it polls is next due PollInterval later, even when the poll fails, so an order whose
// poll keeps failing waits behind the others instead of holding up the batch. The first failure
// ends the tick and is returned.
func (w *Watcher) RunOnce(ctx context.Context) (int, error) {
	now := w.now()
	orders, err := w.store.WatchSet(ctx, now, now.Add(-w.cfg.LateWindow), w.cfg.Batch)
	if err != nil {
		return 0, err
	}
	for i, o := range orders {
		err := errors.Join(w.poll(ctx, o, now), w.store.MarkPolled(ctx, o.ID, new(now.Add(w.cfg.PollInterval))))
		if err != nil {
			return i + 1, fmt.Errorf("poll order %s: %w", o.ID, err)
		}
	}
	return len(orders), nil
}

// poll asks the node for the transactions that list o's reference, and applies each one that the
// ledger does not have yet, oldest first.
func (w *Watcher) poll(ctx context.Context, o order.Order, now time.Time) error {
	listed, err := w.chain.Signatures(ctx, o.Reference, w.cfg.SignatureLimit)
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
		if err != nil {
			return err
		}
		if _, err := w.store.Apply(ctx, o.ID, func(cur order.Order) (Decision, error) { return Decide(cur, c, now) }); err != nil {
			return err
		}
	}
	return nil
}

// candidate fetches the transaction s and returns it with Verify's verdict on it as a payment to o.
func (w *Watcher) candidate(ctx context.Context, o order.Order, s solana.SignatureInfo) (Candidate, error) {
	c := Candidate{Signature: s.Signature, Slot: s.Slot, BlockTime: s.BlockTime}
	tx, err := w.chain.Transaction(ctx, s.Signature)
	if err != nil {
		return Candidate{}, err
	}
	c.Verdict = Verify(tx, w.merchant, o.Reference)
	return c, nil
}
