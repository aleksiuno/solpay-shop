package web_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

// post sends form to path as a browser's form does.
func post(h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return serve(h, r)
}

// requirePage requires w to be an HTML page with status.
func requirePage(t *testing.T, w *httptest.ResponseRecorder, status int) string {
	t.Helper()
	require.Equal(t, status, w.Code, w.Body.String())
	sent := w.Result().Header // as written with the status, not as set later
	assert.Equal(t, "text/html; charset=utf-8", sent.Get("Content-Type"))
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

// The home page is the root only: another path gets the error page.
func TestHomeIsTheRootOnly(t *testing.T) {
	mux, _ := newSite(web.Deps{Products: &catalogue{}})
	assert.Contains(t, requirePage(t, get(mux, "/no-such-page"), http.StatusNotFound), "There is no such page.")
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
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errors.New("server: shutdown timed out"))
	requirePage(t, serve(mux, httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)), http.StatusInternalServerError)
	line := logLine(t, buf)
	assert.Equal(t, "WARN", line["level"])
	assert.Equal(t, "request canceled", line["msg"])
	assert.Equal(t, "server: shutdown timed out", line["cause"], "the context's cause, not only that it ended")
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

// A browser that has the style sheet asks for it by its ETag on every page, and gets a 304 without
// the sheet while it is the same. The tag is made from the sheet's bytes: a fixed one would answer
// 304 to a copy of the sheet from before a deploy, and the browser would keep it.
func TestStaticStylesAreRevalidatedByTheirTag(t *testing.T) {
	mux, _ := newSite(web.Deps{})
	w := get(mux, "/static/style.css")
	require.Equal(t, http.StatusOK, w.Code)
	sum := sha256.Sum256(w.Body.Bytes())
	tag := `"` + hex.EncodeToString(sum[:8]) + `"`
	sent := w.Result().Header // as written with the status, not as set later
	assert.Equal(t, tag, sent.Get("ETag"), "a strong tag made from the sheet")
	assert.Equal(t, "no-cache", sent.Get("Cache-Control"))

	ask := func(ifNoneMatch string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
		r.Header.Set("If-None-Match", ifNoneMatch)
		return serve(mux, r)
	}
	w = ask(tag)
	assert.Equal(t, http.StatusNotModified, w.Code)
	assert.Empty(t, w.Body.String())
	sent = w.Result().Header
	assert.Equal(t, tag, sent.Get("ETag"))
	assert.Equal(t, "no-cache", sent.Get("Cache-Control"))

	w = ask(`"an-older-sheet"`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "font")
}

// A path that no route serves, with any method, gets the error page in the site's layout, as an
// unknown product does.
func TestUnknownPagesGetTheErrorPage(t *testing.T) {
	mux, _ := newSite(web.Deps{Products: &catalogue{}})
	for _, path := range []string{"/no-such-page", "/p/", "/p/go-payments-checklist/more", "/orders/", "/d/"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			body := requirePage(t, serve(mux, httptest.NewRequest(method, path, nil)), http.StatusNotFound)
			assert.Contains(t, body, "There is no such page.", method+" "+path)
			assert.Contains(t, body, `<a href="/">All products</a>`, method+" "+path)
		}
	}
}

// A wrong method on a path that a route serves gets the 405 that ServeMux sends, in plain text
// with the methods the path has, and reaches no handler: a download keeps its plain-text answers.
func TestAWrongMethodIsNotAllowed(t *testing.T) {
	d := &downloads{}
	mux, _ := newSite(web.Deps{Products: &catalogue{err: errBoom}, Downloads: d})
	tests := []struct{ method, path, allow string }{
		{http.MethodPost, "/", "GET, HEAD"},
		{http.MethodPost, "/p/go-payments-checklist", "GET, HEAD"},
		{http.MethodGet, "/checkout", "POST"},
		{http.MethodDelete, "/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e", "GET, HEAD"},
		{http.MethodPut, "/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e/qr.png", "GET, HEAD"},
		{http.MethodPost, link, "GET, HEAD"},
		{http.MethodPost, "/static/style.css", "GET, HEAD"},
	}
	for _, tt := range tests {
		w := serve(mux, httptest.NewRequest(tt.method, tt.path, nil))
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code, tt.path)
		assert.Equal(t, tt.allow, w.Header().Get("Allow"), tt.path)
		assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"), tt.path)
		assert.Equal(t, "Method Not Allowed\n", w.Body.String(), tt.path)
	}
	assert.Empty(t, d.calls, "no download was opened")
}

// In the 405, Allow lists the path's methods as ServeMux lists them, whichever they are. No page
// takes DELETE, PATCH or PUT yet, so the test adds routes that do, to the site's mux and to a bare
// one with the same patterns.
func TestAWrongMethodListsTheMethodsAsServeMuxDoes(t *testing.T) {
	mux, _ := newSite(web.Deps{Products: &catalogue{}})
	bare := http.NewServeMux()
	bare.HandleFunc("GET /p/{slug}", http.NotFound)
	for _, pattern := range []string{"DELETE /p/{slug}", "PATCH /p/{slug}", "PUT /p/{slug}"} {
		mux.HandleFunc(pattern, http.NotFound)
		bare.HandleFunc(pattern, http.NotFound)
	}
	want := serve(bare, httptest.NewRequest(http.MethodPost, "/p/go-payments-checklist", nil))
	require.Equal(t, http.StatusMethodNotAllowed, want.Code)
	require.Equal(t, "DELETE, GET, HEAD, PATCH, PUT", want.Header().Get("Allow"))

	w := serve(mux, httptest.NewRequest(http.MethodPost, "/p/go-payments-checklist", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Equal(t, want.Header().Get("Allow"), w.Header().Get("Allow"))
}

// List cells keep whole words, so a table too wide for the screen scrolls in its box. Only a cell
// marked long breaks inside its value, and a cell marked time keeps the width of a date.
func TestListCellsKeepWholeWords(t *testing.T) {
	mux, _ := newSite(web.Deps{})
	css := get(mux, "/static/style.css").Body.String()
	assert.Contains(t, css, "table.list td.long { min-width: 10rem; overflow-wrap: anywhere; }")
	assert.Contains(t, css, "table.list td.time { min-width: 7.5rem; }")
	assert.NotContains(t, css, "\ntable.list td {", "no rule for every cell")
}
