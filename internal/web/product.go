package web

import (
	"net/http"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/product"
)

// productPage is what the product page shows: the product and, while it is for sale, the
// checkout form, with the quantity it holds and what was wrong with the last one sent.
type productPage struct {
	Product  product.Product
	Quantity string
	Min, Max int
	Problem  string
}

// product shows the product whose slug is in the path. A product that is no longer for sale is
// shown without the form.
func (s site) product(w http.ResponseWriter, r *http.Request) {
	p, err := s.Products.GetBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.showProduct(w, r, http.StatusOK, productPage{Product: p, Quantity: "1"})
}

// showProduct writes the product page with status.
func (s site) showProduct(w http.ResponseWriter, r *http.Request, status int, pp productPage) {
	pp.Min, pp.Max = order.MinQuantity, order.MaxQuantity
	s.render(w, r, status, "product.html", view{Title: pp.Product.Title, Data: pp})
}
