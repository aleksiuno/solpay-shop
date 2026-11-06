package web

import (
	"net/http"
	"strings"
)

// methods are the methods that unknown tries a path with, in the order ServeMux lists them in
// its own Allow header.
var methods = []string{
	http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodPatch, http.MethodPost, http.MethodPut,
}

// unknown returns the handler of catchAll, a pattern on mux that matches every method, so that a
// path no page has gets the error page rather than ServeMux's plain-text 404. A request reaches it
// when no other route serves its method and path.
//
// ServeMux sends its own 405 only when no pattern matches the method and path but one matches the
// path, and the catch-all matches every method and path under it. So unknown tries the path with
// each method: if a route serves it with another, the answer is the 405 that ServeMux would have
// sent, in plain text with an Allow header. It stays plain text because the catch-all for / also
// takes a wrong method on /healthz and the downloads, which answer in plain text. httpapi probes
// its own mux in the same way for its JSON answers; web does not import httpapi.
func (s site) unknown(mux *http.ServeMux, catchAll string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var allow []string
		for _, m := range methods {
			probe := r.WithContext(r.Context()) // a shallow copy, for mux.Handler only
			probe.Method = m
			if _, pattern := mux.Handler(probe); pattern != catchAll && pattern != "" {
				allow = append(allow, m)
			}
		}
		if len(allow) == 0 {
			s.problem(w, r, http.StatusNotFound, "Not found", "There is no such page.")
			return
		}
		w.Header().Set("Allow", strings.Join(allow, ", "))
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	})
}
