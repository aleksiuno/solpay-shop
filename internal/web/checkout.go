package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/aleksiuno/solpay-shop/internal/order"
)

// maxForm is the largest form body that checkout reads.
const maxForm = 4 << 10

// checkout places an order from the product page's form, with the fields product (the slug) and
// quantity, and sends the browser to the order's page with a 303. A quantity that is out of range,
// or not a number, shows the product page again with the problem, 422. A product that is no
// longer for sale gets its page, which says so, with a 409, whatever the quantity.
func (s site) checkout(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		s.problem(w, r, http.StatusBadRequest, "Bad request", "The form could not be read.")
		return
	}
	slug, quantity := r.PostForm.Get("product"), r.PostForm.Get("quantity")
	qty, err := strconv.Atoi(quantity)
	if err != nil {
		qty = 0 // out of range, so the service says what the range is
	}
	o, err := s.Orders.Create(r.Context(), slug, qty)
	var invalid *order.ValidationError
	switch {
	case errors.As(err, &invalid):
		s.refuseCheckout(w, r, http.StatusUnprocessableEntity, slug, quantity, "The number of licences "+invalid.Reason+".")
	case errors.Is(err, order.ErrProductInactive):
		s.refuseCheckout(w, r, http.StatusConflict, slug, quantity, "")
	case err != nil:
		s.fail(w, r, err)
	default:
		http.Redirect(w, r, "/orders/"+o.ID.String(), http.StatusSeeOther)
	}
}

// refuseCheckout shows the product page again with status and the problem, or, for a product no
// longer for sale, its page with a 409.
func (s site) refuseCheckout(w http.ResponseWriter, r *http.Request, status int, slug, quantity, problem string) {
	p, err := s.Products.GetBySlug(r.Context(), slug)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !p.Active {
		status, problem = http.StatusConflict, "" // the page says so
	}
	s.showProduct(w, r, status, productPage{Product: p, Quantity: quantity, Problem: problem})
}
