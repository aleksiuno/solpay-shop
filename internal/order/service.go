package order

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/product"
)

var (
	// ErrNotFound: no order has the id asked for.
	ErrNotFound = errors.New("order: not found")
	// ErrProductInactive: the product exists but is not for sale.
	ErrProductInactive = errors.New("order: product is not for sale")
)

// The quantities an order may have.
const (
	MinQuantity = 1
	MaxQuantity = 10
)

// ValidationError says which input is not acceptable, and why. Field is the input's name in
// snake_case, as forms and the API name it.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return "order: invalid " + e.Field + ": " + e.Reason
}

// ListFilter selects the orders to list. The zero ListFilter matches every order.
type ListFilter struct {
	Status Status // only orders in this status
}

// Repository stores orders.
type Repository interface {
	// Create stores o, and evs in the same transaction.
	Create(ctx context.Context, o Order, evs ...audit.Event) error
	Get(ctx context.Context, id uuid.UUID) (Order, error) // ErrNotFound
	// List returns the newest orders that match f, newest first.
	List(ctx context.Context, f ListFilter) ([]Order, error)
}

// ProductGetter finds a product by its slug, or returns product.ErrNotFound.
type ProductGetter interface {
	GetBySlug(ctx context.Context, slug string) (product.Product, error)
}

// Service creates orders, and gets and lists them.
type Service struct {
	repo     Repository
	products ProductGetter
	ttl      time.Duration
	newRef   func() string
	now      func() time.Time
}

// NewService returns a Service that stores orders in repo and finds products with products. An
// order may be paid for ttl after it is created. newRef returns a fresh payment reference, and now
// the current time in UTC, truncated to the microseconds Postgres stores, so that the times the
// service returns are the ones a later read gives.
func NewService(repo Repository, products ProductGetter, ttl time.Duration, newRef func() string, now func() time.Time) *Service {
	return &Service{repo: repo, products: products, ttl: ttl, newRef: newRef, now: now}
}

// Create places an order for qty licences of the product with slug. The amount is the product's
// price times qty, fixed now, and the order may be paid until ttl from now. The order and its
// order.created event are stored together.
//
// A quantity outside 1 to 10 gives a *ValidationError, an unknown slug product.ErrNotFound, and a
// product that is not for sale ErrProductInactive.
func (s *Service) Create(ctx context.Context, slug string, qty int) (Order, error) {
	if qty < MinQuantity || qty > MaxQuantity {
		return Order{}, &ValidationError{"quantity", fmt.Sprintf("must be from %d to %d", MinQuantity, MaxQuantity)}
	}
	p, err := s.products.GetBySlug(ctx, slug)
	if err != nil {
		return Order{}, err
	}
	if !p.Active {
		return Order{}, ErrProductInactive
	}
	now := s.now()
	o := Order{
		ID:           uuid.NewV4(),
		ProductID:    p.ID,
		ProductSlug:  p.Slug,
		ProductTitle: p.Title,
		Quantity:     qty,
		// A product costs at most 1000 SOL, so ten of them stay far inside int64.
		AmountLamports: p.PriceLamports * int64(qty),
		Reference:      s.newRef(),
		Status:         Pending,
		ExpiresAt:      now.Add(s.ttl),
		CreatedAt:      now,
	}
	ev := audit.Event{
		OrderID: &o.ID,
		Kind:    audit.OrderCreated,
		Message: "order created",
		Data: map[string]any{
			"product_slug":    p.Slug,
			"quantity":        qty,
			"amount_lamports": o.AmountLamports,
			"expires_at":      o.ExpiresAt.UTC().Format(time.RFC3339Nano),
		},
	}
	if err := s.repo.Create(ctx, o, ev); err != nil {
		return Order{}, err
	}
	return o, nil
}

// Get returns the order with id, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Order, error) {
	return s.repo.Get(ctx, id)
}

// List returns the newest orders that match f, newest first.
func (s *Service) List(ctx context.Context, f ListFilter) ([]Order, error) {
	return s.repo.List(ctx, f)
}
