package web_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aleksiuno/solpay-shop/internal/product"
	"github.com/aleksiuno/solpay-shop/internal/web"
)

// newCatalogue returns the web routes over the checklist and a retired product.
func newCatalogue() *http.ServeMux {
	mux, _ := newSite(web.Deps{Products: &catalogue{products: []product.Product{checklist, retired}}})
	return mux
}

func TestProductPage(t *testing.T) {
	body := requirePage(t, get(newCatalogue(), "/p/go-payments-checklist"), http.StatusOK)
	assert.Contains(t, body, "<h1>Go payments checklist</h1>")
	assert.Contains(t, body, "A checklist for payment code in Go.")
	assert.Contains(t, body, "0.005 SOL per licence")
	assert.Contains(t, body, "<title>Go payments checklist · solpay-shop</title>")
}

func TestProductNoLongerForSale(t *testing.T) {
	body := requirePage(t, get(newCatalogue(), "/p/old-notes"), http.StatusOK)
	assert.Contains(t, body, `<p class="notice">This product is no longer for sale.</p>`)
}

func TestProductThatDoesNotExist(t *testing.T) {
	body := requirePage(t, get(newCatalogue(), "/p/no-such-product"), http.StatusNotFound)
	assert.Contains(t, body, "There is no such product.")
}
