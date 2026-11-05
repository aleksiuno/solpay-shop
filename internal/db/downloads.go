package db

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/db/sqlcdb"
	"github.com/aleksiuno/solpay-shop/internal/download"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/product"
)

var _ download.Store = (*Downloads)(nil)

// Downloads reads orders for downloads and marks them fulfilled. Like Orders, it saves the events
// of a change in the change's transaction, and logs them only after the commit.
type Downloads struct {
	pool *pgxpool.Pool
	q    *sqlcdb.Queries
	rec  *audit.Recorder
}

// NewDownloads returns a Downloads that uses pool and logs committed events with rec.
func NewDownloads(pool *pgxpool.Pool, rec *audit.Recorder) *Downloads {
	return &Downloads{pool: pool, q: sqlcdb.New(pool), rec: rec}
}

// Get returns the order with id and the product it bought, or order.ErrNotFound.
func (s *Downloads) Get(ctx context.Context, id uuid.UUID) (order.Order, product.Product, error) {
	r, err := s.q.GetOrder(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return order.Order{}, product.Product{}, order.ErrNotFound
	}
	if err != nil {
		return order.Order{}, product.Product{}, fmt.Errorf("get order: %w", err)
	}
	p, err := s.q.GetProductByID(ctx, r.Order.ProductID)
	if err != nil {
		return order.Order{}, product.Product{}, fmt.Errorf("get the order's product: %w", err)
	}
	return toOrder(r.Order, r.ProductSlug, r.ProductTitle, r.HasLatePayment), toProduct(p), nil
}

// MarkFulfilled moves the order with id from paid to fulfilled at the time at, and stores a
// download.issued event with it, in one transaction. The move passes order.Transition, and the
// UPDATE changes the order only while it is paid, so of two first downloads at once only one
// fulfils it and stores the event. For the other, and for an order in any other status,
// MarkFulfilled changes nothing and returns false.
func (s *Downloads) MarkFulfilled(ctx context.Context, id uuid.UUID, at time.Time) (bool, error) {
	if err := order.Transition(order.Paid, order.Fulfilled); err != nil {
		return false, err
	}
	var evs []audit.Event
	err := inTx(ctx, s.pool, func(q *sqlcdb.Queries) error {
		n, err := q.FulfilOrder(ctx, sqlcdb.FulfilOrderParams{
			ID:          id,
			FromStatus:  string(order.Paid),
			ToStatus:    string(order.Fulfilled),
			FulfilledAt: &at,
		})
		if err != nil {
			return fmt.Errorf("fulfil order: %w", err)
		}
		if n == 0 {
			return nil
		}
		evs = []audit.Event{{OrderID: &id, Kind: audit.DownloadIssued, Message: "download issued"}}
		return insertEvents(ctx, q, evs)
	})
	if err != nil {
		return false, err
	}
	s.rec.Log(ctx, evs...)
	return len(evs) > 0, nil
}
