package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A page in none of the admin's sections, such as one whose path only starts like a section's,
// marks no entry of the navigation bar, and its error page links back to the orders, where the
// admin opens. No route leads to such a page yet, so the test asks for one directly.
func TestAdminPageOutsideEverySection(t *testing.T) {
	for _, path := range []string{"/admin/elsewhere", "/admin/orders-old"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		for _, link := range adminNav(r, true) {
			assert.Empty(t, link.Current, path+": "+link.Href)
		}
		href, text := adminBack(r)
		assert.Equal(t, "/admin/orders", href, path)
		assert.Equal(t, "All orders", text, path)
	}
}
