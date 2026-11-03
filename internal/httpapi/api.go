package httpapi

import (
	"log/slog"
	"net/http"
)

// Deps holds what the API's handlers need.
type Deps struct {
	DB  Pinger // checked by /healthz
	Log *slog.Logger
}

// Register adds the public routes to mux.
func Register(mux *http.ServeMux, d Deps) {
	mux.Handle("GET /healthz", health(d.DB, d.Log))
}
