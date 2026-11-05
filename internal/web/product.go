package web

import (
	"net/http"

	"github.com/aleksiuno/solpay-shop/internal/product"
)

// productPage is what the product page shows.
type productPage struct {
	Product product.Product
}

// product shows the product whose slug is in the path. A product that is no longer for sale is
// shown too, marked as such.
func (s site) product(w http.ResponseWriter, r *http.Request) {
	p, err := s.Products.GetBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "product.html", view{Title: p.Title, Data: productPage{Product: p}})
}
