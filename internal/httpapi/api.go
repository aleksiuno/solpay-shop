package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/product"
)

// Deps holds what the API's handlers need.
type Deps struct {
	DB        Pinger // checked by /healthz
	Products  Products
	Orders    Orders
	Downloads Downloads
	// ProductAdmin, OrderAdmin, Ledger and EventLog serve the admin API.
	ProductAdmin ProductAdmin
	OrderAdmin   OrderAdmin
	Ledger       Ledger
	EventLog     EventLog
	Merchant     string           // the address that receives payments
	Now          func() time.Time // the clock that decides whether an order can still be paid
	Log          *slog.Logger
}

// Products lists and finds products. *product.Service is one.
type Products interface {
	List(ctx context.Context, activeOnly bool) ([]product.Product, error)
	GetBySlug(ctx context.Context, slug string) (product.Product, error)
}

// Orders places and gets orders. *order.Service is one.
type Orders interface {
	Create(ctx context.Context, slug string, qty int) (order.Order, error)
	Get(ctx context.Context, id uuid.UUID) (order.Order, error)
}

// Downloads makes download links for paid orders. *download.Service is one.
type Downloads interface {
	Link(o order.Order) (string, bool)
}

// api serves the JSON API with its Deps.
type api struct {
	Deps
}

// Register adds the public routes to mux.
func Register(mux *http.ServeMux, d Deps) {
	a := api{d}
	mux.Handle("GET /healthz", health(d.DB, d.Log))
	mux.HandleFunc("GET /api/v1/products", a.listProducts)
	mux.HandleFunc("GET /api/v1/products/{slug}", a.getProduct)
	mux.HandleFunc("POST /api/v1/orders", a.createOrder)
	mux.HandleFunc("GET /api/v1/orders/{id}", a.getOrder)
	mux.Handle("/api/v1/", unknown(mux, "/api/v1/"))
}
