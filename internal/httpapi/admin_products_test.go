package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/httpapi"
	"github.com/aleksiuno/solpay-shop/internal/product"
)

// productAdmin holds products. A set err is returned by Create and Update instead of storing,
// and by List. It notes the products it was asked to store and whether List was asked for active
// ones only.
type productAdmin struct {
	products   []product.Product
	err        error
	saved      []product.Product
	activeOnly bool
}

func (a *productAdmin) List(_ context.Context, activeOnly bool) ([]product.Product, error) {
	a.activeOnly = activeOnly
	return a.products, a.err
}

func (a *productAdmin) Create(_ context.Context, p product.Product) (product.Product, error) {
	a.saved = append(a.saved, p)
	if a.err != nil {
		return product.Product{}, a.err
	}
	p.ID = newID
	return p, nil
}

func (a *productAdmin) Update(_ context.Context, p product.Product) (product.Product, error) {
	a.saved = append(a.saved, p)
	if a.err != nil {
		return product.Product{}, a.err
	}
	for _, q := range a.products {
		if q.ID == p.ID {
			return p, nil
		}
	}
	return product.Product{}, product.ErrNotFound
}

var (
	errBoom = errors.New("boom")
	newID   = uuid.MustParse("9a1b2c3d-4e5f-4a6b-8c7d-0e1f2a3b4c5d")
	adminID = uuid.MustParse("0b6c2f0e-5d1a-4f3b-9c8e-2a7d4e6f8b10")
	// stored is a product the admin API holds, not for sale.
	stored = product.Product{
		ID: adminID, Slug: "old-notes", Title: "Old notes", Description: "Retired.",
		PriceLamports: 1_000_000, FileName: "old.md",
	}
)

// newAdminAPI returns the admin routes over a.
func newAdminAPI(a *productAdmin) *http.ServeMux {
	log, _ := newLogger()
	mux := http.NewServeMux()
	httpapi.RegisterAdmin(mux, httpapi.Deps{ProductAdmin: a, Log: log})
	return mux
}

func send(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	return serve(h, httptest.NewRequest(method, path, strings.NewReader(body)))
}

const productBody = `{"slug": "go-payments-checklist", "title": "Go payments checklist",
	"description": "A checklist.", "price_lamports": 5000000, "file_name": "checklist-v2.md", "active": true}`

func TestAdminListsEveryProduct(t *testing.T) {
	a := &productAdmin{products: []product.Product{stored}}
	w := get(newAdminAPI(a), "/api/v1/admin/products")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.False(t, a.activeOnly, "those not for sale too")
	assert.JSONEq(t, `{"products": [{"id": "0b6c2f0e-5d1a-4f3b-9c8e-2a7d4e6f8b10", "slug": "old-notes",
		"title": "Old notes", "description": "Retired.", "price_lamports": 1000000, "active": false,
		"file_name": "old.md"}]}`, w.Body.String())

	w = get(newAdminAPI(&productAdmin{}), "/api/v1/admin/products")
	assert.JSONEq(t, `{"products": []}`, w.Body.String())
}

func TestAdminCreatesAProduct(t *testing.T) {
	a := &productAdmin{}
	w := send(newAdminAPI(a), http.MethodPost, "/api/v1/admin/products", productBody)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.JSONEq(t, `{"id": "9a1b2c3d-4e5f-4a6b-8c7d-0e1f2a3b4c5d", "slug": "go-payments-checklist",
		"title": "Go payments checklist", "description": "A checklist.", "price_lamports": 5000000,
		"active": true, "file_name": "checklist-v2.md"}`, w.Body.String())
	assert.Equal(t, []product.Product{{
		Slug: "go-payments-checklist", Title: "Go payments checklist", Description: "A checklist.",
		PriceLamports: 5_000_000, FileName: "checklist-v2.md", Active: true,
	}}, a.saved)
}

func TestAdminReplacesAProduct(t *testing.T) {
	a := &productAdmin{products: []product.Product{stored}}
	w := send(newAdminAPI(a), http.MethodPut, "/api/v1/admin/products/"+adminID.String(),
		strings.Replace(productBody, `"active": true`, `"active": false`, 1))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"id":"0b6c2f0e-5d1a-4f3b-9c8e-2a7d4e6f8b10"`)
	require.Len(t, a.saved, 1)
	assert.Equal(t, adminID, a.saved[0].ID)
	assert.False(t, a.saved[0].Active)
}

func TestAdminReplaceAProductThatDoesNotExist(t *testing.T) {
	a := &productAdmin{products: []product.Product{stored}}
	for _, id := range []string{newID.String(), "not-a-uuid"} {
		w := send(newAdminAPI(a), http.MethodPut, "/api/v1/admin/products/"+id, productBody)
		assert.Equal(t, http.StatusNotFound, w.Code, id)
		code, _ := envelope(t, w.Body)
		assert.Equal(t, "not_found", code, id)
	}
	assert.Len(t, a.saved, 1, "a malformed id is not asked for")
}

// What the product service refuses, and a body that is not acceptable, get their envelopes.
func TestAdminProductRefusals(t *testing.T) {
	tests := []struct {
		name, body string
		err        error
		status     int
		code, msg  string
	}{
		{"invalid", productBody, &product.ValidationError{Field: "title", Reason: "is required"},
			http.StatusUnprocessableEntity, "validation_failed", "title is required"},
		{"slug taken", productBody, product.ErrSlugTaken, http.StatusConflict, "slug_taken", "another product has this slug"},
		{"no active", strings.Replace(productBody, `, "active": true`, ``, 1), nil,
			http.StatusUnprocessableEntity, "validation_failed", "active is required"},
		{"unknown field", strings.Replace(productBody, `"active"`, `"for_sale": true, "active"`, 1), nil,
			http.StatusBadRequest, "invalid_request", `unknown field "for_sale"`},
		{"wrong type", strings.Replace(productBody, `5000000`, `"0.005"`, 1), nil,
			http.StatusBadRequest, "invalid_request", "price_lamports has the wrong type"},
		{"store failure", productBody, errBoom, http.StatusInternalServerError, "internal", "internal error"},
	}
	for _, tt := range tests {
		for _, method := range []string{http.MethodPost, http.MethodPut} {
			t.Run(tt.name+" "+method, func(t *testing.T) {
				a := &productAdmin{products: []product.Product{stored}, err: tt.err}
				path := "/api/v1/admin/products"
				if method == http.MethodPut {
					path += "/" + adminID.String()
				}
				w := send(newAdminAPI(a), method, path, tt.body)

				assert.Equal(t, tt.status, w.Code)
				code, msg := envelope(t, w.Body)
				assert.Equal(t, tt.code, code)
				assert.Equal(t, tt.msg, msg)
			})
		}
	}
}

func TestAdminListWhenTheStoreFails(t *testing.T) {
	w := get(newAdminAPI(&productAdmin{err: errBoom}), "/api/v1/admin/products")
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// The product paths answer a method they do not serve with 405 and the ones they do.
func TestAdminProductPathsAnswerAWrongMethod(t *testing.T) {
	mux := newAdminAPI(&productAdmin{})
	w := send(mux, http.MethodDelete, "/api/v1/admin/products", "")
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Equal(t, "GET, HEAD, POST", w.Header().Get("Allow"))

	w = send(mux, http.MethodGet, "/api/v1/admin/products/"+adminID.String(), "")
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Equal(t, "PUT", w.Header().Get("Allow"))
}
