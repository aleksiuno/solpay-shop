package order_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/product"
)

// repo keeps orders and their events in memory.
type repo struct {
	orders map[uuid.UUID]order.Order
	events []audit.Event
	calls  int
	err    error // returned by Create
}

func newRepo(os ...order.Order) *repo {
	r := &repo{orders: map[uuid.UUID]order.Order{}}
	for _, o := range os {
		r.orders[o.ID] = o
	}
	return r
}

func (r *repo) Create(_ context.Context, o order.Order, evs ...audit.Event) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	r.orders[o.ID] = o
	r.events = append(r.events, evs...)
	return nil
}

func (r *repo) Get(_ context.Context, id uuid.UUID) (order.Order, error) {
	o, ok := r.orders[id]
	if !ok {
		return order.Order{}, order.ErrNotFound
	}
	return o, nil
}

func (r *repo) List(_ context.Context, f order.ListFilter) ([]order.Order, error) {
	var out []order.Order
	for _, o := range r.orders {
		if f.Status == "" || o.Status == f.Status {
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b order.Order) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}

// products finds products by slug.
type products map[string]product.Product

func (p products) GetBySlug(_ context.Context, slug string) (product.Product, error) {
	pr, ok := p[slug]
	if !ok {
		return product.Product{}, product.ErrNotFound
	}
	return pr, nil
}

var (
	now       = time.Date(2026, 11, 1, 12, 0, 0, 123_456_000, time.UTC)
	checklist = product.Product{
		ID: uuid.NewV4(), Slug: "go-payments-checklist", Title: "Go payments checklist",
		PriceLamports: 5_000_000, Active: true,
	}
	retired = product.Product{
		ID: uuid.NewV4(), Slug: "retired", Title: "Retired", PriceLamports: 1_000_000,
	}
)

// newService returns a service over r whose clock stands at now and whose references count up.
func newService(r *repo) *order.Service {
	refs := 0
	newRef := func() string {
		refs++
		return "ref-" + strings.Repeat("x", refs)
	}
	ps := products{checklist.Slug: checklist, retired.Slug: retired}
	return order.NewService(r, ps, 15*time.Minute, newRef, func() time.Time { return now })
}

func TestCreate(t *testing.T) {
	r := newRepo()
	got, err := newService(r).Create(t.Context(), "go-payments-checklist", 2)
	require.NoError(t, err)

	assert.NotEqual(t, uuid.Nil(), got.ID)
	assert.Equal(t, byte(4), got.ID[6]>>4, "a random version 4 id, because the id is the order's secret")
	assert.Equal(t, order.Order{
		ID:             got.ID,
		ProductID:      checklist.ID,
		ProductSlug:    "go-payments-checklist",
		ProductTitle:   "Go payments checklist",
		Quantity:       2,
		AmountLamports: 10_000_000,
		Reference:      "ref-x",
		Status:         order.Pending,
		ExpiresAt:      now.Add(15 * time.Minute),
		CreatedAt:      now,
	}, got)
	assert.Equal(t, got, r.orders[got.ID], "the stored order")

	require.Len(t, r.events, 1)
	ev := r.events[0]
	assert.Equal(t, audit.OrderCreated, ev.Kind)
	require.NotNil(t, ev.OrderID)
	assert.Equal(t, got.ID, *ev.OrderID)
	assert.Equal(t, map[string]any{
		"product_slug":    "go-payments-checklist",
		"quantity":        2,
		"amount_lamports": int64(10_000_000),
		"expires_at":      "2026-11-01T12:15:00.123456Z",
	}, ev.Data)
}

func TestCreateGivesEachOrderItsOwnIDAndReference(t *testing.T) {
	s := newService(newRepo())
	a, err := s.Create(t.Context(), "go-payments-checklist", 1)
	require.NoError(t, err)
	b, err := s.Create(t.Context(), "go-payments-checklist", 1)
	require.NoError(t, err)
	assert.NotEqual(t, a.ID, b.ID)
	assert.NotEqual(t, a.Reference, b.Reference)
}

func TestCreateQuantity(t *testing.T) {
	for _, qty := range []int{1, 10} {
		o, err := newService(newRepo()).Create(t.Context(), "go-payments-checklist", qty)
		require.NoError(t, err, qty)
		assert.Equal(t, int64(qty)*5_000_000, o.AmountLamports)
	}
	for _, qty := range []int{0, -1, 11} {
		r := newRepo()
		_, err := newService(r).Create(t.Context(), "go-payments-checklist", qty)
		var ve *order.ValidationError
		require.ErrorAs(t, err, &ve, qty)
		assert.Equal(t, "quantity", ve.Field)
		require.EqualError(t, err, "order: invalid quantity: must be from 1 to 10")
		assert.Zero(t, r.calls, "nothing is stored")
	}
}

func TestCreateNeedsAProductForSale(t *testing.T) {
	r := newRepo()
	_, err := newService(r).Create(t.Context(), "missing", 1)
	require.ErrorIs(t, err, product.ErrNotFound)
	_, err = newService(r).Create(t.Context(), "retired", 1)
	require.ErrorIs(t, err, order.ErrProductInactive)
	assert.Zero(t, r.calls)
}

func TestCreateWhenTheStoreFails(t *testing.T) {
	r := newRepo()
	r.err = errors.New("connection refused")
	_, err := newService(r).Create(t.Context(), "go-payments-checklist", 1)
	require.ErrorIs(t, err, r.err)
}

// placed returns an order for checklist in status s, with 3000 lamports credited unless it is
// pending or expired, which have received nothing.
func placed(s order.Status) order.Order {
	var received int64
	if s != order.Pending && s != order.Expired {
		received = 3000
	}
	return order.Order{
		ID: uuid.NewV4(), ProductID: checklist.ID, Quantity: 1, AmountLamports: 5_000_000,
		ReceivedLamports: received, Reference: "ref", Status: s,
		ExpiresAt: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour),
	}
}

func TestGetAndList(t *testing.T) {
	older, newer := placed(order.Paid), placed(order.Pending)
	newer.CreatedAt = older.CreatedAt.Add(time.Minute)
	s := newService(newRepo(older, newer))

	got, err := s.Get(t.Context(), older.ID)
	require.NoError(t, err)
	assert.Equal(t, older, got)
	_, err = s.Get(t.Context(), uuid.NewV4())
	require.ErrorIs(t, err, order.ErrNotFound)

	all, err := s.List(t.Context(), order.ListFilter{})
	require.NoError(t, err)
	assert.Equal(t, []order.Order{newer, older}, all)
	paid, err := s.List(t.Context(), order.ListFilter{Status: order.Paid})
	require.NoError(t, err)
	assert.Equal(t, []order.Order{older}, paid)
}
