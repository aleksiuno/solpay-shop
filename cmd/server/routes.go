package main

import (
	"log/slog"
	"net/http"

	"github.com/aleksiuno/solpay-shop/internal/config"
	"github.com/aleksiuno/solpay-shop/internal/httpapi"
	"github.com/aleksiuno/solpay-shop/internal/web"
)

// routes returns the server's handler: the public routes, the admin routes behind basic auth, and
// the middleware around them all.
//
// Everything under /admin/ and /api/v1/admin/ goes to one admin mux behind one BasicAuth. The root
// mux holds only those two prefixes and the public mux, so a route registered on the public mux
// under either prefix is never reached, and no admin route can skip the check. Cross-origin
// protection refuses a cross-site request with an unsafe method before any route sees it: a
// browser resends basic credentials on its own, so without it another site could submit a form to
// an admin route. For the same reason, the admin routes' answers forbid framing by another site.
func routes(cfg config.Config, log *slog.Logger, site web.Deps, api httpapi.Deps) http.Handler {
	public := http.NewServeMux()
	web.Register(public, site)
	httpapi.Register(public, api)

	admin := http.NewServeMux()
	web.RegisterAdmin(admin, site)
	httpapi.RegisterAdmin(admin)
	guarded := noFrames(httpapi.BasicAuth(cfg.AdminUser, cfg.AdminPass)(admin))

	root := http.NewServeMux()
	root.Handle("/", public)
	root.Handle("/admin/", guarded)
	root.Handle("/api/v1/admin/", guarded)

	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(httpapi.Forbidden))
	return httpapi.RequestID(httpapi.AccessLog(log)(httpapi.Recover(log)(cop.Handler(root))))
}

// noFrames tells browsers not to show h's pages inside another site's frame, so that a page cannot
// trick an admin into clicking a button it hides under its own.
func noFrames(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		h.ServeHTTP(w, r)
	})
}
