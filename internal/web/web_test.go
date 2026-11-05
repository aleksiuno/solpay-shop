package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/product"
	"github.com/aleksiuno/solpay-shop/internal/web"
)

var (
	checklist = product.Product{
		ID: uuid.MustParse("0b6c2f0e-5d1a-4f3b-9c8e-2a7d4e6f8b10"), Slug: "go-payments-checklist",
		Title: "Go payments checklist", Description: "A checklist for payment code in Go.",
		PriceLamports: 5_000_000, FileName: "checklist-v2.md", Active: true,
	}
	retired = product.Product{
		ID: uuid.MustParse("7f3e9a21-4c6b-4d8e-8a1f-5b2c9d0e3f47"), Slug: "old-notes",
		Title: "Old notes", Description: "Retired.", PriceLamports: 1_000_000, FileName: "old.md",
	}
)

// catalogue holds products. A set err is returned by every call instead. It notes whether List
// was asked for active products only.
type catalogue struct {
	products   []product.Product
	err        error
	activeOnly bool
}

func (c *catalogue) List(_ context.Context, activeOnly bool) ([]product.Product, error) {
	c.activeOnly = activeOnly
	if c.err != nil {
		return nil, c.err
	}
	var out []product.Product
	for _, p := range c.products {
		if p.Active || !activeOnly {
			out = append(out, p)
		}
	}
	return out, nil
}

func (c *catalogue) GetBySlug(_ context.Context, slug string) (product.Product, error) {
	if c.err != nil {
		return product.Product{}, c.err
	}
	for _, p := range c.products {
		if p.Slug == slug {
			return p, nil
		}
	}
	return product.Product{}, product.ErrNotFound
}

// newSite returns a mux with the web routes over d, logging to the returned buffer.
func newSite(d web.Deps) (*http.ServeMux, *bytes.Buffer) {
	var buf bytes.Buffer
	d.Log = slog.New(slog.NewJSONHandler(&buf, nil))
	mux := http.NewServeMux()
	web.Register(mux, d)
	return mux, &buf
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	return serve(h, httptest.NewRequest(http.MethodGet, path, nil))
}

// requirePage requires w to be an HTML page with status.
func requirePage(t *testing.T, w *httptest.ResponseRecorder, status int) string {
	t.Helper()
	require.Equal(t, status, w.Code, w.Body.String())
	sent := w.Result().Header // as written with the status, not as set later
	assert.Equal(t, "text/html; charset=utf-8", sent.Get("Content-Type"))
	assert.Equal(t, "nosniff", sent.Get("X-Content-Type-Options"))
	return w.Body.String()
}

// logLine returns the one line in buf.
func logLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line), buf.String())
	return line
}

func TestHomeListsTheProductsForSale(t *testing.T) {
	c := &catalogue{products: []product.Product{checklist, retired}}
	mux, _ := newSite(web.Deps{Products: c})
	body := requirePage(t, get(mux, "/"), http.StatusOK)

	assert.True(t, c.activeOnly)
	assert.Contains(t, body, `<a href="/p/go-payments-checklist">Go payments checklist</a>`)
	assert.Contains(t, body, "A checklist for payment code in Go.")
	assert.Contains(t, body, "0.005 SOL")
	assert.NotContains(t, body, "Old notes")
	assert.Contains(t, body, `<link rel="stylesheet" href="/static/style.css">`)
}

// The home page is the root only: another path is not found.
func TestHomeIsTheRootOnly(t *testing.T) {
	mux, _ := newSite(web.Deps{Products: &catalogue{}})
	assert.Equal(t, http.StatusNotFound, get(mux, "/no-such-page").Code)
}

func TestHomeWithNothingForSale(t *testing.T) {
	mux, _ := newSite(web.Deps{Products: &catalogue{}})
	assert.Contains(t, requirePage(t, get(mux, "/"), http.StatusOK), "Nothing is for sale yet.")
}

// Product text is shown as text, never as markup.
func TestHomeEscapesProductText(t *testing.T) {
	p := checklist
	p.Title = `<script>alert("x")</script>`
	mux, _ := newSite(web.Deps{Products: &catalogue{products: []product.Product{p}}})
	body := requirePage(t, get(mux, "/"), http.StatusOK)
	assert.NotContains(t, body, "<script>")
	assert.Contains(t, body, "&lt;script&gt;")
}

func TestHomeWhenTheStoreFails(t *testing.T) {
	mux, buf := newSite(web.Deps{Products: &catalogue{err: errBoom}})
	body := requirePage(t, get(mux, "/"), http.StatusInternalServerError)
	assert.Contains(t, body, "Something went wrong")
	assert.NotContains(t, body, "boom", "the error is logged, not shown")
	line := logLine(t, buf)
	assert.Equal(t, "ERROR", line["level"])
	assert.Equal(t, "boom", line["error"])
}

func TestHomeWhenTheRequestIsCanceled(t *testing.T) {
	mux, buf := newSite(web.Deps{Products: &catalogue{err: context.Canceled}})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	requirePage(t, serve(mux, httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)), http.StatusInternalServerError)
	line := logLine(t, buf)
	assert.Equal(t, "WARN", line["level"])
	assert.Equal(t, "request canceled", line["msg"])
}

func TestStaticStyles(t *testing.T) {
	mux, _ := newSite(web.Deps{})
	w := get(mux, "/static/style.css")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/css; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), "font")

	for _, path := range []string{"/static/missing.css", "/static/", "/static/..%2ftemplates%2fbase.html"} {
		assert.Equal(t, http.StatusNotFound, get(mux, path).Code, path)
	}
}
