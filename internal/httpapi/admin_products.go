package httpapi

import (
	"context"
	"net/http"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/product"
)

// ProductAdmin lists, creates and updates products for the admin API. *product.Service is one.
type ProductAdmin interface {
	List(ctx context.Context, activeOnly bool) ([]product.Product, error)
	Create(ctx context.Context, p product.Product) (product.Product, error)
	Update(ctx context.Context, p product.Product) (product.Product, error)
}

// adminProductJSON is a product as the admin API shows it: as the public API does, and with the
// name of the file it sells.
type adminProductJSON struct {
	productJSON
	FileName string `json:"file_name"`
}

func newAdminProductJSON(p product.Product) adminProductJSON {
	return adminProductJSON{newProductJSON(p), p.FileName}
}

// productBody is the body that creates or replaces a product. Every field but description is
// required.
type productBody struct {
	Slug          string `json:"slug"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	PriceLamports int64  `json:"price_lamports"`
	FileName      string `json:"file_name"`
	Active        *bool  `json:"active"` // nil when the body leaves it out
}

// listAdminProducts answers with every product, those not for sale included: {"products": [...]}.
func (a api) listAdminProducts(w http.ResponseWriter, r *http.Request) {
	ps, err := a.ProductAdmin.List(r.Context(), false)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := struct {
		Products []adminProductJSON `json:"products"`
	}{make([]adminProductJSON, 0, len(ps))}
	for _, p := range ps {
		out.Products = append(out.Products, newAdminProductJSON(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// createProduct stores a new product from the body and answers 201 with it.
func (a api) createProduct(w http.ResponseWriter, r *http.Request) {
	p, ok := a.readProduct(w, r)
	if !ok {
		return
	}
	p, err := a.ProductAdmin.Create(r.Context(), p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newAdminProductJSON(p))
}

// updateProduct stores the body over the product whose id is in the path, every field replaced,
// and answers with it. An id that is not a UUID names no product.
func (a api) updateProduct(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, product.ErrNotFound)
		return
	}
	p, ok := a.readProduct(w, r)
	if !ok {
		return
	}
	p.ID = id
	p, err = a.ProductAdmin.Update(r.Context(), p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newAdminProductJSON(p))
}

// readProduct reads a product from r's body. If the body is not acceptable, it answers 400 or 422
// and returns false.
func (a api) readProduct(w http.ResponseWriter, r *http.Request) (product.Product, bool) {
	var b productBody
	if msg := decode(w, r, &b); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return product.Product{}, false
	}
	if b.Active == nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "active is required")
		return product.Product{}, false
	}
	return product.Product{
		Slug: b.Slug, Title: b.Title, Description: b.Description, PriceLamports: b.PriceLamports,
		FileName: b.FileName, Active: *b.Active,
	}, true
}
