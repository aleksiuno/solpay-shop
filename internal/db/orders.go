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
)

// OrdersPageSize is the most orders that Orders.List returns.
const OrdersPageSize = 100

// Orders stores orders. It implements order.Repository. Every change it makes is one transaction
// that also saves the change's events, and it logs those events only after the commit.
type Orders struct {
	pool *pgxpool.Pool
	q    *sqlcdb.Queries
	rec  *audit.Recorder
}

var _ order.Repository = (*Orders)(nil)

// NewOrders returns an Orders that uses pool and logs committed events with rec.
func NewOrders(pool *pgxpool.Pool, rec *audit.Recorder) *Orders {
	return &Orders{pool: pool, q: sqlcdb.New(pool), rec: rec}
}

// Create stores o, due for its first poll at its creation time, and evs in one transaction.
func (s *Orders) Create(ctx context.Context, o order.Order, evs ...audit.Event) error {
	if o.Quantity < 0 || o.Quantity > math.MaxInt32 {
		return fmt.Errorf("create order: quantity %d out of range", o.Quantity)
	}
	qty := int32(o.Quantity)
	err := inTx(ctx, s.pool, func(q *sqlcdb.Queries) error {
		err := q.CreateOrder(ctx, sqlcdb.CreateOrderParams{
			ID:             o.ID,
			ProductID:      o.ProductID,
			Quantity:       qty,
			AmountLamports: o.AmountLamports,
			Reference:      o.Reference,
			Status:         string(o.Status),
			ExpiresAt:      o.ExpiresAt,
			CreatedAt:      o.CreatedAt,
		})
		if err != nil {
			return fmt.Errorf("insert order: %w", err)
		}
		return insertEvents(ctx, q, evs)
	})
	if err != nil {
		return fmt.Errorf("create order: %w", err)
	}
	s.rec.Log(ctx, evs...)
	return nil
}

// Get returns the order with id, or order.ErrNotFound.
func (s *Orders) Get(ctx context.Context, id uuid.UUID) (order.Order, error) {
	r, err := s.q.GetOrder(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return order.Order{}, order.ErrNotFound
	}
	if err != nil {
		return order.Order{}, fmt.Errorf("get order: %w", err)
	}
	return toOrder(r.Order, r.ProductSlug, r.ProductTitle, r.HasLatePayment), nil
}

// List returns up to OrdersPageSize orders that match f, newest first.
func (s *Orders) List(ctx context.Context, f order.ListFilter) ([]order.Order, error) {
	p := sqlcdb.ListOrdersParams{PageSize: OrdersPageSize}
	if f.Status != "" {
		status := string(f.Status)
		p.Status = &status
	}
	rows, err := s.q.ListOrders(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	out := make([]order.Order, 0, len(rows))
	for _, r := range rows {
		out = append(out, toOrder(r.Order, r.ProductSlug, r.ProductTitle, r.HasLatePayment))
	}
	return out, nil
}

// Refund locks the order with id, then reads it and passes it to check. Because the read follows
// the lock, check sees everything that committed while Refund waited for it, a late payment
// included. If check returns no error, Refund stores the order as refunded at the time at, with
// the events check returns, in one transaction, and logs the events after the commit. The status
// change passes order.Transition, and the UPDATE changes the row only if it is still in the status
// read under the lock; otherwise Refund returns order.ErrStaleState. An error from check rolls
// everything back and is returned as it is.
func (s *Orders) Refund(ctx context.Context, id uuid.UUID, at time.Time, check func(order.Order) ([]audit.Event, error)) (order.Order, error) {
	var refunded order.Order
	var evs []audit.Event
	err := inTx(ctx, s.pool, func(q *sqlcdb.Queries) error {
		_, err := q.LockOrder(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return order.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock order: %w", err)
		}
		r, err := q.GetOrder(ctx, id)
		if err != nil {
			return fmt.Errorf("get locked order: %w", err)
		}
		o := toOrder(r.Order, r.ProductSlug, r.ProductTitle, r.HasLatePayment)
		if evs, err = check(o); err != nil {
			return err
		}
		if err := order.Transition(o.Status, order.Refunded); err != nil {
			return err
		}
		n, err := q.RefundOrder(ctx, sqlcdb.RefundOrderParams{
			ID:         id,
			FromStatus: string(o.Status),
			ToStatus:   string(order.Refunded),
			RefundedAt: &at,
		})
		if err != nil {
			return fmt.Errorf("refund order: %w", err)
		}
		if n != 1 {
			return order.ErrStaleState
		}
		if err := insertEvents(ctx, q, evs); err != nil {
			return err
		}
		o.Status, o.RefundedAt = order.Refunded, &at
		refunded = o
		return nil
	})
	if err != nil {
		return order.Order{}, err
	}
	s.rec.Log(ctx, evs...)
	return refunded, nil
}

func toOrder(r sqlcdb.Order, productSlug, productTitle string, hasLatePayment bool) order.Order {
	return order.Order{
		ID:               r.ID,
		ProductID:        r.ProductID,
		ProductSlug:      productSlug,
		ProductTitle:     productTitle,
		Quantity:         int(r.Quantity),
		AmountLamports:   r.AmountLamports,
		ReceivedLamports: r.ReceivedLamports,
		Reference:        r.Reference,
		Status:           order.Status(r.Status),
		ExpiresAt:        r.ExpiresAt,
		CreatedAt:        r.CreatedAt,
		PaidAt:           r.PaidAt,
		FulfilledAt:      r.FulfilledAt,
		RefundedAt:       r.RefundedAt,
		HasLatePayment:   hasLatePayment,
	}
}
