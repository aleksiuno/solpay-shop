// Package web serves the shop's HTML pages and the downloads of paid orders. Its handlers read the
// request, call a service and write the answer; the rules live in the services.
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/download"
	"github.com/aleksiuno/solpay-shop/internal/money"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/product"
)

// Deps holds what the web handlers need.
type Deps struct {
	Products  Products
	Orders    Orders
	Downloads Downloads
	Merchant  string           // the address that receives payments
	Now       func() time.Time // the clock that decides whether an order can still be paid
	// ExpiryGrace is how long after its deadline the watcher still polls a pending order before it
	// expires it, as payment.Config has it. An underpaid order's page reloads itself as long.
	ExpiryGrace time.Duration
	// ProductAdmin, ProductFiles, OrderAdmin, Ledger and EventLog serve the admin pages.
	ProductAdmin ProductAdmin
	ProductFiles FileLister
	OrderAdmin   OrderAdmin
	Ledger       Ledger
	EventLog     EventLog
	Log          *slog.Logger
}

// Products lists and finds products. *product.Service is one.
type Products interface {
	List(ctx context.Context, activeOnly bool) ([]product.Product, error)
	GetBySlug(ctx context.Context, slug string) (product.Product, error)
}

// Orders places and gets orders. *order.Service is one.
type Orders interface {
	Create(ctx context.Context, slug string, qty int) (order.Order, error)
	Get(ctx context.Context, id uuid.UUID) (order.Order, error)
}

// Downloads makes download links and opens the files of paid orders. *download.Service is one.
type Downloads interface {
	Link(o order.Order) (string, bool)
	Open(ctx context.Context, id uuid.UUID, exp, sig string, fulfil bool) (download.File, error)
}

//go:embed templates static
var assets embed.FS

// funcs are the functions the templates may call.
var funcs = template.FuncMap{
	"sol": money.FormatSOL, // lamports as decimal SOL
	"utc": func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05 UTC") },
	// json writes an event's data as one line of JSON. It leaves & and < as they are, since
	// html/template escapes the text it is shown in.
	"json": func(v any) (string, error) {
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		err := enc.Encode(v)
		return strings.TrimSuffix(b.String(), "\n"), err
	},
}

// site serves the web routes with its Deps and the parsed pages, by file name.
type site struct {
	Deps
	pages map[string]*template.Template
}

// Register adds the public routes to mux.
func Register(mux *http.ServeMux, d Deps) {
	s := site{Deps: d, pages: parsePages()}
	mux.HandleFunc("GET /static/style.css", styles)
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /p/{slug}", s.product)
	mux.HandleFunc("POST /checkout", s.checkout)
	mux.HandleFunc("GET /orders/{id}", s.order)
	mux.HandleFunc("GET /orders/{id}/qr.png", s.orderQR)
	mux.HandleFunc("GET /d/{id}", s.download)
}

// parsePages parses each page in templates with the base layout. The templates are embedded, so
// one that does not parse fails every test.
func parsePages() map[string]*template.Template {
	pages := map[string]*template.Template{}
	for _, name := range []string{
		"home.html", "product.html", "order.html", "error.html", "admin_products.html", "admin_product_form.html",
		"admin_orders.html", "admin_order.html", "admin_events.html",
	} {
		pages[name] = template.Must(template.New(name).Funcs(funcs).ParseFS(assets, "templates/base.html", "templates/"+name))
	}
	return pages
}

// styles sends the style sheet, the one static file.
func styles(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, assets, "static/style.css")
}

// view is what the base layout shows around a page: its title, whether it reloads itself every
// 5 seconds, whether it takes the full width for a wide table, and the page's own data.
type view struct {
	Title   string
	Refresh bool
	Wide    bool
	Data    any
}

// render writes page with v and status. It renders into a buffer first, so a template that fails
// gives a 500 instead of half a page.
func (s site) render(w http.ResponseWriter, r *http.Request, status int, page string, v view) {
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, "base", v); err != nil {
		s.Log.LogAttrs(r.Context(), slog.LevelError, "page render failed", slog.String("page", page), slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	// An error here means the client has gone; there is no one left to tell.
	_, _ = buf.WriteTo(w)
}

// problem is what the error page says.
type problem struct {
	Heading, Message string
}

// fail answers err, which a service returned, with the error page that fits it. An error it does
// not know is a 500, and is logged instead of shown.
func (s site) fail(w http.ResponseWriter, r *http.Request, err error) {
	ctx := r.Context()
	switch {
	case errors.Is(err, product.ErrNotFound):
		s.problem(w, r, http.StatusNotFound, "Not found", "There is no such product.")
	case errors.Is(err, order.ErrNotFound):
		s.problem(w, r, http.StatusNotFound, "Not found", "There is no such order.")
	case ctx.Err() != nil:
		// The client hung up, or the server is stopping; no one reads the answer.
		s.Log.LogAttrs(ctx, slog.LevelWarn, "request canceled",
			slog.Any("error", err), slog.Any("cause", context.Cause(ctx)))
		s.problem(w, r, http.StatusInternalServerError, "Something went wrong", "Please try again.")
	default:
		s.Log.LogAttrs(ctx, slog.LevelError, "request failed", slog.Any("error", err))
		s.problem(w, r, http.StatusInternalServerError, "Something went wrong", "Please try again.")
	}
}

// problem answers with the error page.
func (s site) problem(w http.ResponseWriter, r *http.Request, status int, heading, message string) {
	s.render(w, r, status, "error.html", view{Title: heading, Data: problem{heading, message}})
}
