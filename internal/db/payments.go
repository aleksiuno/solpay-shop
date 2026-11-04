package db

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/db/sqlcdb"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
)

// Payments stores the payment ledger and what payments do to orders. Like Orders, every change it
// makes is one transaction that also saves the change's events, and it logs those events only
// after the commit.
type Payments struct {
	pool *pgxpool.Pool
	q    *sqlcdb.Queries
	rec  *audit.Recorder
}

// NewPayments returns a Payments that uses pool and logs committed events with rec.
func NewPayments(pool *pgxpool.Pool, rec *audit.Recorder) *Payments {
	return &Payments{pool: pool, q: sqlcdb.New(pool), rec: rec}
}

// Apply locks the order with orderID, reads it, passes it to decide, and stores the decision in
// one transaction: the ledger row, the order's new status, received total and paid time, and the
// decision's events. Because the read follows the lock, decide sees every payment that committed
// while Apply waited.
//
// The ledger row goes in first, and the signature is unique across all orders. If the signature is
// already there, Apply stores only a payment.duplicate_ignored event and returns false: the order
// and its received total stay as they were, so a transaction is credited once, however often, and
// by however many callers at once, it is applied. An unknown order gives order.ErrNotFound, and
// an error from decide rolls everything back and is returned as it is.
func (s *Payments) Apply(ctx context.Context, orderID uuid.UUID, decide func(order.Order) (payment.Decision, error)) (bool, error) {
	var evs []audit.Event
	applied := false
	err := inTx(ctx, s.pool, func(q *sqlcdb.Queries) error {
		_, err := q.LockOrder(ctx, orderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return order.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock order: %w", err)
		}
		r, err := q.GetOrder(ctx, orderID)
		if err != nil {
			return fmt.Errorf("get locked order: %w", err)
		}
		o := toOrder(r.Order, r.ProductSlug, r.ProductTitle, r.HasLatePayment)
		d, err := decide(o)
		if err != nil {
			return err
		}
		if d.Payment.OrderID != orderID {
			return fmt.Errorf("decision for order %s applied to order %s", d.Payment.OrderID, orderID)
		}

		_, err = q.InsertPayment(ctx, insertPaymentParams(d.Payment))
		if errors.Is(err, pgx.ErrNoRows) {
			evs = []audit.Event{{
				OrderID:   &o.ID,
				Kind:      audit.PaymentDuplicateIgnored,
				Message:   "transaction already in the ledger",
				Signature: d.Payment.Signature,
			}}
			return insertEvents(ctx, q, evs)
		}
		if err != nil {
			return fmt.Errorf("insert payment: %w", err)
		}

		if d.Status != o.Status || d.Received != o.ReceivedLamports {
			if d.Status != o.Status {
				if err := order.Transition(o.Status, d.Status); err != nil {
					return err
				}
			}
			n, err := q.UpdateOrderPayment(ctx, sqlcdb.UpdateOrderPaymentParams{
				ID:               orderID,
				FromStatus:       string(o.Status),
				ToStatus:         string(d.Status),
				ReceivedLamports: d.Received,
				PaidAt:           d.PaidAt,
			})
			if err != nil {
				return fmt.Errorf("update order: %w", err)
			}
			if n != 1 {
				return order.ErrStaleState
			}
		}
		evs = d.Events
		applied = true
		return insertEvents(ctx, q, evs)
	})
	if err != nil {
		return false, err
	}
	s.rec.Log(ctx, evs...)
	return applied, nil
}

// KnownSignatures returns the set of those of sigs that are already in the ledger.
func (s *Payments) KnownSignatures(ctx context.Context, sigs []string) (map[string]bool, error) {
	found, err := s.q.KnownSignatures(ctx, sigs)
	if err != nil {
		return nil, fmt.Errorf("known signatures: %w", err)
	}
	known := make(map[string]bool, len(found))
	for _, sig := range found {
		known[sig] = true
	}
	return known, nil
}

// WatchSet returns up to limit orders that are due for a poll at now, the longest due first: every
// pending order, and each underpaid or expired one whose deadline is after lateCutoff. An order is
// due once its next poll time is not after now. Create sets that time to the order's creation time,
// and MarkPolled moves it. Only the orders table is read, so the orders' product slug and title
// are empty and HasLatePayment is false; Apply reads an order in full, under its lock, before it
// changes anything.
func (s *Payments) WatchSet(ctx context.Context, now, lateCutoff time.Time, limit int) ([]order.Order, error) {
	if limit < 0 || limit > math.MaxInt32 {
		return nil, fmt.Errorf("watch set: limit %d out of range", limit)
	}
	rows, err := s.q.WatchSet(ctx, sqlcdb.WatchSetParams{Now: now, LateCutoff: lateCutoff, BatchSize: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("watch set: %w", err)
	}
	out := make([]order.Order, 0, len(rows))
	for _, r := range rows {
		out = append(out, toOrder(r, "", "", false))
	}
	return out, nil
}

// MarkPolled sets when the order with orderID is next due for a poll. A nil next means it needs no
// more polls, so WatchSet never returns it again. An unknown order gives order.ErrNotFound.
func (s *Payments) MarkPolled(ctx context.Context, orderID uuid.UUID, next *time.Time) error {
	n, err := s.q.MarkPolled(ctx, sqlcdb.MarkPolledParams{ID: orderID, NextPollAt: next})
	if err != nil {
		return fmt.Errorf("mark polled: %w", err)
	}
	if n == 0 {
		return order.ErrNotFound
	}
	return nil
}

// StopPolling ends the polling of every underpaid or expired order whose deadline is at or before
// lateCutoff, as MarkPolled with a nil time does, and returns how many orders it changed. WatchSet
// leaves such an order out, so if its last poll came after its late window had ended, because
// polling ran late or the shop was down, the order would keep its next poll time, and its entry
// in the index that WatchSet reads, for good.
func (s *Payments) StopPolling(ctx context.Context, lateCutoff time.Time) (int64, error) {
	n, err := s.q.StopPolling(ctx, lateCutoff)
	if err != nil {
		return 0, fmt.Errorf("stop polling: %w", err)
	}
	return n, nil
}

func insertPaymentParams(p payment.Payment) sqlcdb.InsertPaymentParams {
	var reason *string
	if p.Reason != "" {
		r := string(p.Reason)
		reason = &r
	}
	return sqlcdb.InsertPaymentParams{
		Signature: p.Signature,
		OrderID:   p.OrderID,
		Outcome:   string(p.Outcome),
		Lamports:  p.Lamports,
		Slot:      p.Slot,
		BlockTime: p.BlockTime,
		Reason:    reason,
	}
}
