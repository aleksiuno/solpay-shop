package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/httpapi"
	"github.com/aleksiuno/solpay-shop/internal/product"
)

// catalogue is an in-memory product service. A set err is returned by every call.
type catalogue struct {
	all        []product.Product
	err        error
	activeOnly []bool // what each List call asked for
}

func (c *catalogue) List(_ context.Context, activeOnly bool) ([]product.Product, error) {
	c.activeOnly = append(c.activeOnly, activeOnly)
	if c.err != nil {
		return nil, c.err
	}
	var out []product.Product
	for _, p := range c.all {
		if p.Active || !activeOnly {
			out = append(out, p)
		}
	}
	return out, nil
}

func (c *catalogue) GetBySlug(_ context.Context, slug string) (product.Product, error) {
	if c.err != nil {
		return product.Product{}, c.err
	}
	for _, p := range c.all {
		if p.Slug == slug {
			return p, nil
		}
	}
	return product.Product{}, product.ErrNotFound
}

var (
	checklist = product.Product{
		ID:            uuid.MustParse("0b5d7a52-4c1e-4a9e-9d43-6f1f0c2e8a10"),
		Slug:          "go-payments-checklist",
		Title:         "Go payments checklist",
		Description:   "A checklist for payment code in Go.",
		PriceLamports: 5_000_000,
		FileName:      "go-payments-checklist.md",
		Active:        true,
	}
	withdrawn = product.Product{
		ID:            uuid.MustParse("7f3c2e1d-9b8a-4c6d-8e5f-1a2b3c4d5e6f"),
		Slug:          "old-notes",
		Title:         "Old notes",
		PriceLamports: 1_000_000,
		FileName:      "old-notes.md",
	}
)

const (
	checklistJSON = `{"id": "0b5d7a52-4c1e-4a9e-9d43-6f1f0c2e8a10", "slug": "go-payments-checklist",
		"title": "Go payments checklist", "description": "A checklist for payment code in Go.",
		"price_lamports": 5000000, "active": true}`
	withdrawnJSON = `{"id": "7f3c2e1d-9b8a-4c6d-8e5f-1a2b3c4d5e6f", "slug": "old-notes", "title": "Old notes",
		"description": "", "price_lamports": 1000000, "active": false}`
)

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	return serve(h, httptest.NewRequest(http.MethodGet, path, nil))
}

func TestListProductsShowsThoseForSale(t *testing.T) {
	c := &catalogue{all: []product.Product{checklist, withdrawn}}
	mux, _ := newAPI(httpapi.Deps{Products: c})
	w := get(mux, "/api/v1/products")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"products": [`+checklistJSON+`]}`, w.Body.String(), "no file name, nothing withdrawn")
	assert.Equal(t, []bool{true}, c.activeOnly)
}

func TestListProductsWhenNoneAreForSale(t *testing.T) {
	mux, _ := newAPI(httpapi.Deps{Products: &catalogue{all: []product.Product{withdrawn}}})
	w := get(mux, "/api/v1/products")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"products": []}`, w.Body.String(), "an empty list, not null")
}

func TestGetProduct(t *testing.T) {
	mux, _ := newAPI(httpapi.Deps{Products: &catalogue{all: []product.Product{checklist, withdrawn}}})
	tests := []struct {
		path string
		code int
		body string
	}{
		{"/api/v1/products/go-payments-checklist", http.StatusOK, checklistJSON},
		{"/api/v1/products/old-notes", http.StatusOK, withdrawnJSON},
		{"/api/v1/products/no-such-product", http.StatusNotFound,
			`{"error": {"code": "not_found", "message": "no such product"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			w := get(mux, tt.path)
			assert.Equal(t, tt.code, w.Code)
			assert.JSONEq(t, tt.body, w.Body.String())
		})
	}
}

// A failure under a request whose own context has ended (the client hung up, or the server is
// stopping) is logged as canceled, not as an error of the shop, whatever the error says. The line
// carries the context's cause, which tells the two apart: a client that hung up leaves a plain
// context.Canceled, and the server cuts requests off with a cause of its own.
func TestProductsWhenTheRequestIsCanceled(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		cause error // what ended the request's context; nil leaves it live
		level string
		msg   string
	}{
		{"a canceled request, its error wrapping the cancel", fmt.Errorf("list products: %w", context.Canceled),
			context.Canceled, "WARN", "request canceled"},
		{"a canceled request with another error", errors.New("conn closed"), context.Canceled, "WARN", "request canceled"},
		{"a request the server cut off", fmt.Errorf("list products: %w", context.Canceled),
			errors.New("server: shutdown timed out"), "WARN", "request canceled"},
		{"a live request with an error that wraps a cancel", fmt.Errorf("list products: %w", context.Canceled), nil,
			"ERROR", "request failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux, buf := newAPI(httpapi.Deps{Products: &catalogue{err: tt.err}})
			ctx, cancel := context.WithCancelCause(t.Context())
			if tt.cause != nil {
				cancel(tt.cause)
			}
			defer cancel(nil)
			w := serve(mux, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/products", nil))

			assert.Equal(t, http.StatusInternalServerError, w.Code)
			lines := logLines(t, buf)
			require.Len(t, lines, 1)
			assert.Equal(t, tt.level, lines[0]["level"])
			assert.Equal(t, tt.msg, lines[0]["msg"])
			if tt.cause != nil {
				assert.Equal(t, tt.cause.Error(), lines[0]["cause"], "the context's cause, not only that it ended")
			}
		})
	}
}

func TestProductsWhenTheStoreFails(t *testing.T) {
	for _, path := range []string{"/api/v1/products", "/api/v1/products/go-payments-checklist"} {
		t.Run(path, func(t *testing.T) {
			mux, buf := newAPI(httpapi.Deps{Products: &catalogue{err: errors.New("connection reset")}})
			w := serve(httpapi.RequestID(mux), httptest.NewRequest(http.MethodGet, path, nil))

			assert.Equal(t, http.StatusInternalServerError, w.Code)
			assert.JSONEq(t, `{"error": {"code": "internal", "message": "internal error"}}`, w.Body.String())
			lines := logLines(t, buf)
			require.Len(t, lines, 1)
			assert.Equal(t, "ERROR", lines[0]["level"])
			assert.Equal(t, "connection reset", lines[0]["error"], "logged, not sent")
			assert.Equal(t, w.Header().Get("X-Request-ID"), lines[0]["request_id"])
		})
	}
}
