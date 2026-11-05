// Package web serves what a browser fetches outside the JSON API: the file of a paid order, by
// its download link. Its handlers read the request, call a service and write the answer; the rules
// live in the services.
package web

import (
	"context"
	"log/slog"
	"net/http"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/download"
)

// Deps holds what the web handlers need.
type Deps struct {
	Downloads Downloads
	Log       *slog.Logger
}

// Downloads opens the files of paid orders. *download.Service is one.
type Downloads interface {
	Open(ctx context.Context, id uuid.UUID, exp, sig string, fulfil bool) (download.File, error)
}

// site serves the web routes with its Deps.
type site struct {
	Deps
}

// Register adds the public routes to mux.
func Register(mux *http.ServeMux, d Deps) {
	s := site{d}
	mux.HandleFunc("GET /d/{id}", s.download)
}
