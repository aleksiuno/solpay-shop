package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/config"
	"github.com/aleksiuno/solpay-shop/internal/download"
	"github.com/aleksiuno/solpay-shop/internal/httpapi"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
	"github.com/aleksiuno/solpay-shop/internal/product"
	"github.com/aleksiuno/solpay-shop/internal/solana/solanatest"
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

// placed is a pending order, due 15 minutes after it was placed.
var placed = order.Order{
	ID: uuid.MustParse("3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e"), ProductSlug: "go-payments-checklist",
	ProductTitle: "Go payments checklist", Quantity: 1, AmountLamports: 5_000_000,
	Reference: solanatest.Key("reference"), Status: order.Pending,
	CreatedAt: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2026, 11, 6, 12, 15, 0, 0, time.UTC),
}

// orderBook holds one order, which every lookup finds, with no payments.
type orderBook struct{ o order.Order }

func (b orderBook) Create(context.Context, string, int) (order.Order, error) { return b.o, nil }

func (b orderBook) Get(context.Context, uuid.UUID) (order.Order, error) { return b.o, nil }

func (b orderBook) List(context.Context, order.ListFilter) ([]order.Order, error) {
	return []order.Order{b.o}, nil
}

func (b orderBook) Refund(context.Context, uuid.UUID, string) (order.Order, error) { return b.o, nil }

func (orderBook) Ledger(context.Context, uuid.UUID) ([]payment.Payment, error) { return nil, nil }

// eventLog holds the one event of placing its order.
type eventLog struct{ o order.Order }

func (l eventLog) List(context.Context, audit.ListFilter) ([]audit.Event, error) {
	ev := audit.Event{ID: 1, OrderID: &l.o.ID, Kind: audit.OrderCreated, Message: "order created", CreatedAt: l.o.CreatedAt}
	return []audit.Event{ev}, nil
}

// newShop returns the server's real handler over a shop that holds the placed order and its
// event, so that the routes that show an order answer 200. Its clock stands a minute after the
// order was placed, while the order can be paid. Its download service, as newRouter's, has no
// store and no files.
func newShop() http.Handler {
	log := slog.New(slog.DiscardHandler)
	clock := func() time.Time { return placed.CreatedAt.Add(time.Minute) }
	book, events := orderBook{placed}, eventLog{placed}
	downloads := download.NewService(nil, nil, []byte("test-only-download-secret-0123456789"), clock)
	merchant := solanatest.Key("merchant")
	return routes(cfg, log,
		web.Deps{
			Products: catalogue{}, Orders: book, Downloads: downloads, Merchant: merchant, Now: clock,
			OrderAdmin: book, Ledger: book, EventLog: events, Log: log,
		},
		httpapi.Deps{
			DB: pinger{}, Products: catalogue{}, Orders: book, Downloads: downloads, OrderAdmin: book,
			Ledger: book, EventLog: events, Merchant: merchant, Now: clock, Log: log,
		})
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
	"/api/v1/admin/events",
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

// The admin routes' answers, with or without credentials, forbid framing by another site, and no
// cache may keep them.
func TestAdminRoutesForbidFramingAndCaching(t *testing.T) {
	h := newRouter()
	for _, prepare := range [][]func(*http.Request){nil, {withAuth(adminUser, adminPass)}} {
		for _, path := range []string{"/admin/no-such-page", "/api/v1/admin/no-such-endpoint"} {
			sent := request(h, http.MethodGet, path, prepare...).Result().Header
			assert.Equal(t, "DENY", sent.Get("X-Frame-Options"), path)
			assert.Equal(t, "no-store", sent.Get("Cache-Control"), path)
		}
	}
}

// Each admin route is mounted behind the credentials: a wrong method on its path gets the 405 that
// names the methods it serves. The catch-alls for /admin/ and /api/v1/admin/ give that 405 only for
// a path that a registered pattern serves, and a 404 for any other.
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
		{http.MethodPost, "/api/v1/admin/events", "GET, HEAD"},
	}
	for _, tt := range tests {
		w := request(h, tt.method, tt.path, withAuth(adminUser, adminPass))
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code, tt.path)
		assert.Equal(t, tt.allow, w.Header().Get("Allow"), tt.path)
		assert.Equal(t, "DENY", w.Result().Header.Get("X-Frame-Options"), tt.path)
		assert.Equal(t, "no-store", w.Result().Header.Get("Cache-Control"), tt.path)
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
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"), method)
	}
}

// fileDownloads is a download service whose every link opens the file at path.
type fileDownloads struct{ path string }

func (fileDownloads) Link(order.Order) (string, bool) { return "", false }

func (d fileDownloads) Open(context.Context, uuid.UUID, string, string, bool) (download.File, error) {
	f, err := os.Open(d.path)
	if err != nil {
		return download.File{}, err
	}
	return download.File{Name: "go-payments-checklist.md", Content: f}, nil
}

// A download through the server's whole handler outlives the write timeout that pages keep: its
// own deadline reaches the server's writer past every middleware, so one added later that hid the
// writer would fail this. The client reads nothing until that timeout has passed, by which time
// the file fills the socket's buffers many times over.
func TestDownloadsOutliveTheWriteTimeout(t *testing.T) {
	content := bytes.Repeat([]byte("x"), 16<<20)
	path := filepath.Join(t.TempDir(), "checklist.md")
	require.NoError(t, os.WriteFile(path, content, 0o600))
	log := slog.New(slog.DiscardHandler)
	srv := httptest.NewUnstartedServer(routes(cfg, log, web.Deps{Downloads: fileDownloads{path}, Log: log},
		httpapi.Deps{Log: log}))
	srv.Config.WriteTimeout = 100 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + "/d/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e?exp=1793534400&sig=c2lnbmF0dXJl")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	time.Sleep(3 * srv.Config.WriteTimeout)
	n, err := io.Copy(io.Discard, resp.Body)
	require.NoError(t, err, "read %d of %d bytes", n, len(content))
	assert.Equal(t, int64(len(content)), n)
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

// A path that no route serves gets the shop's error page, under /admin/ once past the credentials.
// A wrong method on a path that a route serves gets a plain-text 405 with Allow, as ServeMux sends
// it, so /healthz and the downloads keep their plain-text answers.
func TestUnknownPagesUseTheErrorPage(t *testing.T) {
	h := newRouter()
	tests := []struct {
		method, path       string
		code               int
		allow, contentType string
		body               string
	}{
		{http.MethodGet, "/no-such-page", http.StatusNotFound, "", "text/html; charset=utf-8", "There is no such page."},
		{http.MethodPost, "/p/x", http.StatusMethodNotAllowed, "GET, HEAD", "text/plain; charset=utf-8", "Method Not Allowed"},
		{http.MethodPost, "/healthz", http.StatusMethodNotAllowed, "GET, HEAD", "text/plain; charset=utf-8", "Method Not Allowed"},
		{http.MethodPost, "/d/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e", http.StatusMethodNotAllowed, "GET, HEAD", "text/plain; charset=utf-8", "Method Not Allowed"},
		{http.MethodGet, "/admin/no-such-page", http.StatusNotFound, "", "text/html; charset=utf-8", "There is no such page."},
		{http.MethodDelete, "/admin/orders", http.StatusMethodNotAllowed, "GET, HEAD", "text/plain; charset=utf-8", "Method Not Allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			w := request(h, tt.method, tt.path, withAuth(adminUser, adminPass))
			assert.Equal(t, tt.code, w.Code)
			assert.Equal(t, tt.allow, w.Header().Get("Allow"))
			assert.Equal(t, tt.contentType, w.Header().Get("Content-Type"))
			assert.Contains(t, w.Body.String(), tt.body)
		})
	}
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
// id outside them all, so every line carries it. The security headers are set outside Recover as
// well, so the 500 keeps them.
func TestMiddlewareOrder(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(httpapi.ContextHandler{Handler: slog.NewJSONHandler(&buf, nil)})
	h := routes(cfg, log, web.Deps{Log: log}, httpapi.Deps{DB: panicker{}, Log: log})
	w := request(h, http.MethodGet, "/healthz")
	refused := request(h, http.MethodPost, "/healthz", crossSite)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "internal", errorCode(t, w))
	sent := w.Result().Header // as written with the status, not as set later
	assert.Equal(t, "nosniff", sent.Get("X-Content-Type-Options"))
	assert.Equal(t, "same-origin", sent.Get("Referrer-Policy"))
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

// Every response carries a request id and the security headers, whoever writes it: a handler, the
// mux, basic auth or cross-origin protection.
func TestEveryResponseHasTheCommonHeaders(t *testing.T) {
	h := newRouter()
	tests := []struct {
		method, path string
		prepare      []func(*http.Request)
	}{
		{http.MethodGet, "/healthz", nil},
		{http.MethodGet, "/static/style.css", nil},
		{http.MethodGet, "/no-such-page", nil},
		{http.MethodGet, "/admin/", nil},
		{http.MethodPost, "/healthz", []func(*http.Request){crossSite}},
	}
	for _, tt := range tests {
		sent := request(h, tt.method, tt.path, tt.prepare...).Result().Header
		assert.Len(t, sent.Get("X-Request-ID"), 36, tt.method+" "+tt.path)
		assert.Equal(t, "nosniff", sent.Get("X-Content-Type-Options"), tt.method+" "+tt.path)
		assert.Equal(t, "same-origin", sent.Get("Referrer-Policy"), tt.method+" "+tt.path)
	}
}

// Each kind of route adds its own headers to the common ones. No cache may keep an admin answer,
// an answer that shows an order, a download's answer or the health check's; the style sheet is
// checked on every page; and only the admin routes forbid framing.
func TestHeadersByRouteKind(t *testing.T) {
	h := newShop()
	id := placed.ID.String()
	tests := []struct {
		name, path string
		creds      bool // with the admin credentials
		status     int
		cache      string // the Cache-Control
		frames     string // the X-Frame-Options
	}{
		{"home page", "/", false, http.StatusOK, "", ""},
		{"order page", "/orders/" + id, false, http.StatusOK, "no-store", ""},
		{"QR code", "/orders/" + id + "/qr.png", false, http.StatusOK, "no-store", ""},
		{"download by a forged link", "/d/" + id + "?exp=1793534400&sig=c2lnbmF0dXJl", false, http.StatusForbidden, "no-store", ""},
		{"API products", "/api/v1/products", false, http.StatusOK, "", ""},
		{"API order", "/api/v1/orders/" + id, false, http.StatusOK, "no-store", ""},
		{"health check", "/healthz", false, http.StatusOK, "no-store", ""},
		{"style sheet", "/static/style.css", false, http.StatusOK, "no-cache", ""},
		{"admin order list", "/admin/orders", true, http.StatusOK, "no-store", "DENY"},
		{"admin order page", "/admin/orders/" + id, true, http.StatusOK, "no-store", "DENY"},
		{"admin event log", "/admin/events", true, http.StatusOK, "no-store", "DENY"},
		{"admin API orders", "/api/v1/admin/orders", true, http.StatusOK, "no-store", "DENY"},
		{"admin API order", "/api/v1/admin/orders/" + id, true, http.StatusOK, "no-store", "DENY"},
		{"admin API events", "/api/v1/admin/events", true, http.StatusOK, "no-store", "DENY"},
		{"admin page without credentials", "/admin/orders", false, http.StatusUnauthorized, "no-store", "DENY"},
		{"admin API without credentials", "/api/v1/admin/events", false, http.StatusUnauthorized, "no-store", "DENY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var prepare []func(*http.Request)
			if tt.creds {
				prepare = append(prepare, withAuth(adminUser, adminPass))
			}
			w := request(h, http.MethodGet, tt.path, prepare...)
			require.Equal(t, tt.status, w.Code, w.Body.String())
			sent := w.Result().Header // as written with the status, not as set later
			assert.Equal(t, "nosniff", sent.Get("X-Content-Type-Options"))
			assert.Equal(t, "same-origin", sent.Get("Referrer-Policy"))
			assert.Equal(t, tt.cache, sent.Get("Cache-Control"))
			assert.Equal(t, tt.frames, sent.Get("X-Frame-Options"))
		})
	}
}

// The clock that the order service and the API share is in UTC, in the microseconds that Postgres
// keeps, so the times in a 201 are the ones a later read returns.
func TestNow(t *testing.T) {
	assert.Equal(t, time.UTC, now().Location())
	in := time.Date(2026, 11, 3, 14, 0, 0, 123_456_789, time.FixedZone("EET", 2*60*60))
	assert.Equal(t, time.Date(2026, 11, 3, 12, 0, 0, 123_456_000, time.UTC), utcMicros(in))
}
