package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/config"
	"github.com/aleksiuno/solpay-shop/internal/download"
	"github.com/aleksiuno/solpay-shop/internal/httpapi"
	"github.com/aleksiuno/solpay-shop/internal/product"
	"github.com/aleksiuno/solpay-shop/internal/web"
)

const (
	adminUser = "admin"
	adminPass = "test-only-password"
)

type pinger struct{}

func (pinger) Ping(context.Context) error { return nil }

// catalogue is a product service with nothing for sale.
type catalogue struct{}

func (catalogue) List(context.Context, bool) ([]product.Product, error) { return nil, nil }

func (catalogue) GetBySlug(context.Context, string) (product.Product, error) {
	return product.Product{}, product.ErrNotFound
}

// panicker is a database whose Ping panics.
type panicker struct{}

func (panicker) Ping(context.Context) error { panic("boom") }

var cfg = config.Config{AdminUser: adminUser, AdminPass: adminPass}

// newRouter returns the server's real handler, with test credentials and a database that answers.
// Its download service has no store and no files, which a link it did not sign never reaches.
func newRouter() http.Handler {
	log := slog.New(slog.DiscardHandler)
	downloads := download.NewService(nil, nil, []byte("test-only-download-secret-0123456789"), now)
	return routes(cfg, log, web.Deps{Products: catalogue{}, Downloads: downloads, Log: log},
		httpapi.Deps{DB: pinger{}, Products: catalogue{}, Log: log})
}

func request(h http.Handler, method, path string, prepare ...func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	for _, p := range prepare {
		p(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func withAuth(user, pass string) func(*http.Request) {
	return func(r *http.Request) { r.SetBasicAuth(user, pass) }
}

func crossSite(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }

// errorCode returns the code of the JSON error envelope in w's body.
func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct{ Error struct{ Code string } }
	require.NoError(t, json.NewDecoder(w.Body).Decode(&e), w.Body.String())
	return e.Error.Code
}

// adminPaths are the paths under each admin mount that the tests try. Each admin route joins
// this list when it is added.
var adminPaths = []string{
	"/admin/",
	"/admin/no-such-page",
	"/admin/orders",
	"/admin/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e",
	"/admin/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e/refund",
	"/admin/events",
	"/admin/products",
	"/admin/products/new",
	"/admin/products/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e",
	"/admin/products/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e/edit",
	"/api/v1/admin/",
	"/api/v1/admin/no-such-endpoint",
	"/api/v1/admin/products",
	"/api/v1/admin/products/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e",
	"/api/v1/admin/orders",
	"/api/v1/admin/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e",
	"/api/v1/admin/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e/refund",
}

func TestAdminRoutesNeedCredentials(t *testing.T) {
	h := newRouter()
	creds := map[string][]func(*http.Request){
		"none":           nil,
		"wrong user":     {withAuth("root", adminPass)},
		"wrong password": {withAuth(adminUser, "wrong-password")},
	}
	for _, path := range adminPaths {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
			for name, prepare := range creds {
				t.Run(method+" "+path+" "+name, func(t *testing.T) {
					w := request(h, method, path, prepare...)
					assert.Equal(t, http.StatusUnauthorized, w.Code)
					assert.Equal(t, `Basic realm="solpay-shop admin"`, w.Header().Get("WWW-Authenticate"))
					assert.Equal(t, "unauthorized", errorCode(t, w))
				})
			}
		}
	}
}

// The admin routes' answers, with or without credentials, forbid framing by another site.
func TestAdminRoutesForbidFraming(t *testing.T) {
	h := newRouter()
	for _, prepare := range [][]func(*http.Request){nil, {withAuth(adminUser, adminPass)}} {
		for _, path := range []string{"/admin/no-such-page", "/api/v1/admin/no-such-endpoint"} {
			w := request(h, http.MethodGet, path, prepare...)
			assert.Equal(t, "DENY", w.Result().Header.Get("X-Frame-Options"), path)
		}
	}
}

// Each admin route is mounted behind the credentials: a wrong method on its path gets the 405 that
// names the methods it serves, which only a registered pattern gives.
func TestAdminRoutesAreMounted(t *testing.T) {
	h := newRouter()
	tests := []struct{ method, path, allow string }{
		{http.MethodPost, "/admin/", "GET, HEAD"},
		{http.MethodDelete, "/admin/orders", "GET, HEAD"},
		{http.MethodDelete, "/admin/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e", "GET, HEAD"},
		{http.MethodGet, "/admin/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e/refund", "POST"},
		{http.MethodPost, "/admin/events", "GET, HEAD"},
		{http.MethodDelete, "/admin/products", "GET, HEAD, POST"},
		{http.MethodDelete, "/admin/products/new", "GET, HEAD, POST"},
		{http.MethodGet, "/admin/products/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e", "POST"},
		{http.MethodDelete, "/admin/products/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e/edit", "GET, HEAD"},
		{http.MethodDelete, "/api/v1/admin/products", "GET, HEAD, POST"},
		{http.MethodGet, "/api/v1/admin/products/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e", "PUT"},
		{http.MethodPost, "/api/v1/admin/orders", "GET, HEAD"},
		{http.MethodPost, "/api/v1/admin/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e", "GET, HEAD"},
		{http.MethodGet, "/api/v1/admin/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e/refund", "POST"},
	}
	for _, tt := range tests {
		w := request(h, tt.method, tt.path, withAuth(adminUser, adminPass))
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code, tt.path)
		assert.Equal(t, tt.allow, w.Header().Get("Allow"), tt.path)
		assert.Equal(t, "DENY", w.Result().Header.Get("X-Frame-Options"), tt.path)
	}
}

func TestAdminRoutesWithCredentials(t *testing.T) {
	h := newRouter()
	w := request(h, http.MethodGet, "/api/v1/admin/no-such-endpoint", withAuth(adminUser, adminPass))
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "not_found", errorCode(t, w), "the admin API answers in JSON")

	w = request(h, http.MethodGet, "/admin/no-such-page", withAuth(adminUser, adminPass))
	assert.Equal(t, http.StatusNotFound, w.Code, "past the credentials, to the admin mux")
}

func TestPublicRoutesNeedNoCredentials(t *testing.T) {
	h := newRouter()
	w := request(h, http.MethodGet, "/healthz")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ok\n", w.Body.String())

	w = request(h, http.MethodGet, "/api/v1/products")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"products": []}`, w.Body.String())

	w = request(h, http.MethodGet, "/")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Nothing is for sale yet.", "the shop's home page")
}

// A cross-site form cannot place an order, through the API or the shop's checkout: the refusal
// comes before the order service, which newRouter leaves unset.
func TestCrossSiteOrdersAreRefused(t *testing.T) {
	for _, path := range []string{"/api/v1/orders", "/checkout"} {
		w := request(newRouter(), http.MethodPost, path, crossSite)
		assert.Equal(t, http.StatusForbidden, w.Code, path)
		assert.Equal(t, "forbidden", errorCode(t, w), path)
	}
}

// A download link that the shop did not sign is refused in plain text, before the store is read.
// A link followed from another site, such as a mail, reaches the download, since GET is safe.
func TestForgedDownloadLinksAreRefused(t *testing.T) {
	h := newRouter()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := request(h, method, "/d/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e?exp=1793534400&sig=c2lnbmF0dXJl", crossSite)
		assert.Equal(t, http.StatusForbidden, w.Code, method)
		assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"), "the download's refusal")
		assert.Equal(t, "private, no-store", w.Header().Get("Cache-Control"), method)
	}
}

func TestTheAPIAnswersUnknownPathsInJSON(t *testing.T) {
	h := newRouter()
	w := request(h, http.MethodGet, "/api/v1/no-such-endpoint")
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "not_found", errorCode(t, w))

	w = request(h, http.MethodPost, "/api/v1/products")
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Equal(t, "GET, HEAD", w.Header().Get("Allow"))
	assert.Equal(t, "method_not_allowed", errorCode(t, w))
}

func TestCrossOriginUnsafeRequestsAreRefused(t *testing.T) {
	h := newRouter()
	for _, path := range []string{
		"/healthz", "/api/v1/admin/no-such-endpoint", "/admin/no-such-page",
		"/admin/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e/refund",
		"/api/v1/admin/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e/refund",
	} {
		w := request(h, http.MethodPost, path, crossSite, withAuth(adminUser, adminPass))
		assert.Equal(t, http.StatusForbidden, w.Code, path)
		assert.Equal(t, "forbidden", errorCode(t, w), path)
	}

	w := request(h, http.MethodGet, "/healthz", crossSite)
	assert.Equal(t, http.StatusOK, w.Code, "a safe method is allowed")
	w = request(h, http.MethodPost, "/api/v1/admin/no-such-endpoint", withAuth(adminUser, adminPass),
		func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") })
	assert.Equal(t, http.StatusNotFound, w.Code, "a same-origin request passes")
}

// A panic and a refused cross-site request show the order of the middleware: Recover and
// cross-origin protection inside the access log, which sees the 500 and the 403, and the request
// id outside them all, so every line carries it.
func TestMiddlewareOrder(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(httpapi.ContextHandler{Handler: slog.NewJSONHandler(&buf, nil)})
	h := routes(cfg, log, web.Deps{Log: log}, httpapi.Deps{DB: panicker{}, Log: log})
	w := request(h, http.MethodGet, "/healthz")
	refused := request(h, http.MethodPost, "/healthz", crossSite)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "internal", errorCode(t, w))
	assert.Equal(t, http.StatusForbidden, refused.Code)
	id := w.Header().Get("X-Request-ID")
	var lines []map[string]any
	for line := range strings.Lines(buf.String()) {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m), line)
		lines = append(lines, m)
	}
	require.Len(t, lines, 3)
	assert.Equal(t, "handler panicked", lines[0]["msg"])
	assert.Equal(t, id, lines[0]["request_id"])
	assert.Equal(t, "request", lines[1]["msg"])
	assert.InDelta(t, http.StatusInternalServerError, lines[1]["status"], 0)
	assert.Equal(t, id, lines[1]["request_id"])
	assert.Equal(t, "request", lines[2]["msg"])
	assert.InDelta(t, http.StatusForbidden, lines[2]["status"], 0)
	assert.Equal(t, refused.Header().Get("X-Request-ID"), lines[2]["request_id"])
}

func TestEveryResponseHasARequestID(t *testing.T) {
	h := newRouter()
	for _, w := range []*httptest.ResponseRecorder{
		request(h, http.MethodGet, "/healthz"),
		request(h, http.MethodGet, "/no-such-page"),
		request(h, http.MethodGet, "/admin/"),
		request(h, http.MethodPost, "/healthz", crossSite),
	} {
		assert.Len(t, w.Header().Get("X-Request-ID"), 36)
	}
}

// The clock that the order service and the API share is in UTC, in the microseconds that Postgres
// keeps, so the times in a 201 are the ones a later read returns.
func TestNow(t *testing.T) {
	assert.Equal(t, time.UTC, now().Location())
	in := time.Date(2026, 11, 3, 14, 0, 0, 123_456_789, time.FixedZone("EET", 2*60*60))
	assert.Equal(t, time.Date(2026, 11, 3, 12, 0, 0, 123_456_000, time.UTC), utcMicros(in))
}
