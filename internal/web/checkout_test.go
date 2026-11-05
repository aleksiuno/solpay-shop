package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/product"
	"github.com/aleksiuno/solpay-shop/internal/web"
)

// newShop returns the web routes over the checklist, a retired product and orders.
func newShop(o *orders) *http.ServeMux {
	mux, _ := newSite(web.Deps{Products: &catalogue{products: []product.Product{checklist, retired}}, Orders: o})
	return mux
}

func TestProductPageHasTheCheckoutForm(t *testing.T) {
	body := requirePage(t, get(newShop(&orders{}), "/p/go-payments-checklist"), http.StatusOK)
	assert.Contains(t, body, `<form class="checkout" method="post" action="/checkout">`)
	assert.Contains(t, body, `<input type="hidden" name="product" value="go-payments-checklist">`)
	assert.Contains(t, body, `name="quantity" type="number" min="1" max="10" value="1"`)
	assert.NotContains(t, body, `class="problem"`)

	body = requirePage(t, get(newShop(&orders{}), "/p/old-notes"), http.StatusOK)
	assert.NotContains(t, body, "<form", "none for a product no longer for sale")
}

func TestCheckout(t *testing.T) {
	o := &orders{order: order.Order{ID: orderID}}
	w := post(newShop(o), "/checkout", url.Values{"product": {"go-payments-checklist"}, "quantity": {"2"}})

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e", w.Header().Get("Location"))
	assert.Equal(t, "go-payments-checklist", o.slug)
	assert.Equal(t, 2, o.qty)
}

// A quantity that the order service refuses, or one that is not a number, shows the product page
// again with the problem and the quantity as sent.
func TestCheckoutRefusesAQuantity(t *testing.T) {
	tests := []struct {
		quantity string
		asked    int // the quantity the service was asked for
	}{
		{"0", 0},
		{"11", 11},
		{"two", 0},
		{"", 0},
	}
	for _, tt := range tests {
		t.Run(tt.quantity, func(t *testing.T) {
			o := &orders{err: &order.ValidationError{Field: "quantity", Reason: "must be from 1 to 10"}}
			w := post(newShop(o), "/checkout", url.Values{"product": {"go-payments-checklist"}, "quantity": {tt.quantity}})

			body := requirePage(t, w, http.StatusUnprocessableEntity)
			assert.Equal(t, tt.asked, o.qty)
			assert.Contains(t, body, `<p class="problem" role="alert">The number of licences must be from 1 to 10.</p>`)
			assert.Contains(t, body, `value="`+tt.quantity+`" required>`)
			assert.Contains(t, body, "<form", "to try again")
		})
	}
}

// A product no longer for sale gets its page, which says so, with a 409, whatever the quantity:
// the order service checks the quantity first.
func TestCheckoutAProductNoLongerForSale(t *testing.T) {
	for _, err := range []error{order.ErrProductInactive, &order.ValidationError{Field: "quantity", Reason: "must be from 1 to 10"}} {
		o := &orders{err: err}
		w := post(newShop(o), "/checkout", url.Values{"product": {"old-notes"}, "quantity": {"0"}})

		body := requirePage(t, w, http.StatusConflict)
		assert.Contains(t, body, `<p class="notice">This product is no longer for sale.</p>`)
		assert.Equal(t, 1, strings.Count(body, "no longer for sale"), err)
		assert.NotContains(t, body, `class="problem"`, err)
		assert.NotContains(t, body, "<form", err)
	}
}

func TestCheckoutAProductThatDoesNotExist(t *testing.T) {
	o := &orders{err: product.ErrNotFound}
	w := post(newShop(o), "/checkout", url.Values{"product": {"no-such-product"}, "quantity": {"1"}})
	assert.Contains(t, requirePage(t, w, http.StatusNotFound), "There is no such product.")
}

func TestCheckoutWhenTheStoreFails(t *testing.T) {
	o := &orders{err: errBoom}
	w := post(newShop(o), "/checkout", url.Values{"product": {"go-payments-checklist"}, "quantity": {"1"}})
	assert.Contains(t, requirePage(t, w, http.StatusInternalServerError), "Something went wrong")
}

func TestCheckoutRefusesAFormItCannotRead(t *testing.T) {
	o := &orders{}
	w := post(newShop(o), "/checkout", url.Values{"product": {strings.Repeat("x", 5000)}, "quantity": {"1"}})

	require.Contains(t, requirePage(t, w, http.StatusBadRequest), "The form could not be read.")
	assert.Zero(t, o.creates)
}
