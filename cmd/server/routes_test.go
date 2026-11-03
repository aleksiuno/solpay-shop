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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/config"
	"github.com/aleksiuno/solpay-shop/internal/httpapi"
	"github.com/aleksiuno/solpay-shop/internal/product"
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
func newRouter() http.Handler {
	log := slog.New(slog.DiscardHandler)
	return routes(cfg, log, httpapi.Deps{DB: pinger{}, Products: catalogue{}, Log: log})
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
	"/api/v1/admin/",
	"/api/v1/admin/no-such-endpoint",
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
	for _, path := range []string{"/healthz", "/api/v1/admin/no-such-endpoint", "/admin/no-such-page"} {
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
	h := routes(cfg, log, httpapi.Deps{DB: panicker{}, Log: log})
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
