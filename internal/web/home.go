package web

import "net/http"

// home lists the products for sale.
func (s site) home(w http.ResponseWriter, r *http.Request) {
	ps, err := s.Products.List(r.Context(), true)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "home.html", view{Title: "Products", Data: ps})
}
