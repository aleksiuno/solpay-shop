package order

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/product"
)

var (
	// ErrNotFound: no order has the id asked for.
	ErrNotFound = errors.New("order: not found")
	// ErrProductInactive: the product exists but is not for sale.
	ErrProductInactive = errors.New("order: product is not for sale")
	// ErrNoLatePayment: an expired order received nothing, so there is nothing to refund. It wraps
	// ErrInvalidTransition.
	ErrNoLatePayment = fmt.Errorf("%w: an expired order can be refunded only after a late payment", ErrInvalidTransition)
	// ErrStaleState: the order's status changed between reading it and storing the change.
	ErrStaleState = errors.New("order: status changed meanwhile")
)

// The limits that Create and Refund check.
const (
	MinQuantity   = 1
	MaxQuantity   = 10
	MaxNoteLength = 500 // in characters
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
	// Refund locks the order with id and passes check the order as it is under the lock, late
	// payments included. If check returns no error, Refund stores the order as refunded at the time
	// at, with the events check returns, in one transaction, and returns the stored order. An error
	// from check rolls everything back and is returned as it is. ErrNotFound, ErrStaleState.
	Refund(ctx context.Context, id uuid.UUID, at time.Time, check func(Order) ([]audit.Event, error)) (Order, error)
}

// ProductGetter finds a product by its slug, or returns product.ErrNotFound.
type ProductGetter interface {
	GetBySlug(ctx context.Context, slug string) (product.Product, error)
}

// Service creates orders, gets and lists them, and refunds them.
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

// Refund marks the order with id refunded, with an optional note from the admin, and records an
// order.refunded event with it. The SOL itself is returned outside the shop.
//
// An order can be refunded once it has received something: when it is underpaid, paid or
// fulfilled, or expired with a late payment. Any other order gives an error wrapping
// ErrInvalidTransition, an expired order without a late payment gives ErrNoLatePayment, and an
// unknown id gives ErrNotFound. The note is trimmed of surrounding white space first; a note that
// is then longer than 500 characters, or has a NUL or a byte that is not UTF-8, gives a
// *ValidationError.
func (s *Service) Refund(ctx context.Context, id uuid.UUID, note string) (Order, error) {
	note = strings.TrimSpace(note)
	if !utf8.ValidString(note) || strings.ContainsRune(note, 0) {
		return Order{}, &ValidationError{"note", "must be valid UTF-8 text without NUL characters"}
	}
	if utf8.RuneCountInString(note) > MaxNoteLength {
		return Order{}, &ValidationError{"note", fmt.Sprintf("is longer than %d characters", MaxNoteLength)}
	}
	return s.repo.Refund(ctx, id, s.now(), func(o Order) ([]audit.Event, error) {
		if o.Status == Expired && !o.HasLatePayment {
			return nil, ErrNoLatePayment
		}
		if err := Transition(o.Status, Refunded); err != nil {
			return nil, err
		}
		data := map[string]any{"from_status": string(o.Status), "received_lamports": o.ReceivedLamports}
		if note != "" {
			data["note"] = note
		}
		return []audit.Event{{OrderID: &o.ID, Kind: audit.OrderRefunded, Message: "order refunded", Data: data}}, nil
	})
}
