package httpapi

import "net/http"

// RegisterAdmin adds the admin API routes to mux, which the caller guards with BasicAuth. A path
// under /api/v1/admin/ that names no endpoint gets a not_found envelope rather than the mux's
// plain-text 404.
func RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/admin/", notFound)
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, codeNotFound, "no such endpoint")
}
