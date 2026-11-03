package httpapi

import (
	"net/http"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/product"
)

// productJSON is a product as the API shows it. The name of the file it sells stays private.
type productJSON struct {
	ID            uuid.UUID `json:"id"`
	Slug          string    `json:"slug"`
	Title         string    `json:"title"`
	Description   string    `json:"description"`
	PriceLamports int64     `json:"price_lamports"`
	Active        bool      `json:"active"`
}

func newProductJSON(p product.Product) productJSON {
	return productJSON{
		ID:            p.ID,
		Slug:          p.Slug,
		Title:         p.Title,
		Description:   p.Description,
		PriceLamports: p.PriceLamports,
		Active:        p.Active,
	}
}

// listProducts answers with the products for sale: {"products": [...]}.
func (a api) listProducts(w http.ResponseWriter, r *http.Request) {
	ps, err := a.Products.List(r.Context(), true)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := struct {
		Products []productJSON `json:"products"`
	}{make([]productJSON, 0, len(ps))}
	for _, p := range ps {
		out.Products = append(out.Products, newProductJSON(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// getProduct answers with the product whose slug is in the path. A product that is not for sale
// is shown too, with active false, so an old link tells the client that, rather than that the
// product never existed.
func (a api) getProduct(w http.ResponseWriter, r *http.Request) {
	p, err := a.Products.GetBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newProductJSON(p))
}
