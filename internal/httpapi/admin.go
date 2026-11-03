package httpapi

import (
	"net/http"
	"strings"
)

// RegisterAdmin adds the admin API routes to mux, which the caller guards with BasicAuth. A path
// under /api/v1/admin/ that no route serves gets a JSON envelope, as under /api/v1/.
func RegisterAdmin(mux *http.ServeMux) {
	mux.Handle("/api/v1/admin/", unknown(mux, "/api/v1/admin/"))
}

// methods are the methods that unknown tries a path with.
var methods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
}

// unknown returns the handler of catchAll, a pattern on mux that matches every method. A request
// reaches it when no other route serves its method and path. If a route serves the path with
// another method, unknown answers 405 method_not_allowed with an Allow header; otherwise 404
// not_found. ServeMux sends its own 405 only when no pattern matches the method and path but one
// matches the path, and the catch-all matches every method and path under it.
func unknown(mux *http.ServeMux, catchAll string) http.Handler {
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
			writeError(w, http.StatusNotFound, codeNotFound, "no such endpoint")
			return
		}
		w.Header().Set("Allow", strings.Join(allow, ", "))
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed")
	})
}
