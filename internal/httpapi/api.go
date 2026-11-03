package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/aleksiuno/solpay-shop/internal/product"
)

// Deps holds what the API's handlers need.
type Deps struct {
	DB       Pinger // checked by /healthz
	Products Products
	Log      *slog.Logger
}

// Products lists and finds products. *product.Service is one.
type Products interface {
	List(ctx context.Context, activeOnly bool) ([]product.Product, error)
	GetBySlug(ctx context.Context, slug string) (product.Product, error)
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
}
