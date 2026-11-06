package web_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/product"
	"github.com/aleksiuno/solpay-shop/internal/web"
)

// productAdmin holds products by id. A set err is returned by Create and Update instead of
// storing, and listErr by List. It notes the products it was asked to store and whether List
// was asked for active ones only.
type productAdmin struct {
	products   []product.Product
	err        error
	listErr    error
	getErr     error
	saved      []product.Product
	activeOnly bool
}

func (a *productAdmin) List(_ context.Context, activeOnly bool) ([]product.Product, error) {
	a.activeOnly = activeOnly
	return a.products, a.listErr
}

func (a *productAdmin) GetByID(_ context.Context, id uuid.UUID) (product.Product, error) {
	if a.getErr != nil {
		return product.Product{}, a.getErr
	}
	for _, p := range a.products {
		if p.ID == id {
			return p, nil
		}
	}
	return product.Product{}, product.ErrNotFound
}

func (a *productAdmin) Create(_ context.Context, p product.Product) (product.Product, error) {
	a.saved = append(a.saved, p)
	return p, a.err
}

func (a *productAdmin) Update(ctx context.Context, p product.Product) (product.Product, error) {
	a.saved = append(a.saved, p)
	if a.err != nil {
		return product.Product{}, a.err
	}
	if _, err := a.GetByID(ctx, p.ID); err != nil {
		return product.Product{}, err
	}
	return p, nil
}

// fileList lists the files that products can sell, or fails with err.
type fileList struct {
	names []string
	err   error
}

func (f fileList) List() ([]string, error) { return f.names, f.err }

// newAdmin returns the admin pages over a, with the checklist's file and another one in the
// files directory, and one whose name the product rules refuse.
func newAdmin(a *productAdmin) *http.ServeMux {
	files := fileList{names: []string{"checklist-v2.md", "my notes.md", "notes.md"}}
	return adminOver(web.Deps{ProductAdmin: a, ProductFiles: files})
}

// adminOver returns the admin pages over d, with a logger that discards.
func adminOver(d web.Deps) *http.ServeMux {
	d.Log = slog.New(slog.DiscardHandler)
	mux := http.NewServeMux()
	web.RegisterAdmin(mux, d)
	return mux
}

// navBar is the admin's navigation bar on a page of the section whose list is at section, with
// current as that entry's aria-current.
func navBar(section, current string) string {
	bar := `<nav class="admin" aria-label="Admin">`
	for _, e := range []struct{ path, text string }{{"/admin/orders", "Orders"}, {"/admin/events", "Events"}, {"/admin/products", "Products"}} {
		attr := ""
		if e.path == section {
			attr = ` aria-current="` + current + `"`
		}
		bar += "\n<a href=\"" + e.path + "\"" + attr + ">" + e.text + "</a>"
	}
	return bar + "\n</nav>"
}

// Every admin page, an error page too, has the one navigation bar, which marks the page's
// section: "page" on the section's list, "true" on its other pages, the error page in place of
// the list among them. The shop's pages have none.
func TestAdminPagesShareNavigation(t *testing.T) {
	now := deadline.Add(time.Hour)
	mux := adminOver(web.Deps{
		ProductAdmin: &productAdmin{products: []product.Product{checklist}}, ProductFiles: fileList{names: []string{"checklist-v2.md"}},
		OrderAdmin: &orderAdmin{orders: []order.Order{pending}, refundErr: order.ErrStaleState}, Ledger: ledger{}, EventLog: &eventLog{},
		Now: func() time.Time { return now },
	})
	badPrice := productForm()
	badPrice.Set("price", "five")
	orderPath, productPath := "/admin/orders/"+pending.ID.String(), "/admin/products/"+checklist.ID.String()
	tests := []struct {
		path    string
		form    url.Values // posted when set
		status  int
		section string // the list page of the page's section
		current string
	}{
		{"/admin/orders", nil, http.StatusOK, "/admin/orders", "page"},
		{"/admin/orders?status=paid", nil, http.StatusOK, "/admin/orders", "page"},
		{"/admin/orders?status=lost", nil, http.StatusBadRequest, "/admin/orders", "page"},
		{orderPath, nil, http.StatusOK, "/admin/orders", "true"},
		{orderPath + "/refund", url.Values{}, http.StatusConflict, "/admin/orders", "true"},
		{"/admin/orders/" + retired.ID.String(), nil, http.StatusNotFound, "/admin/orders", "true"},
		{"/admin/events", nil, http.StatusOK, "/admin/events", "page"},
		{"/admin/events?level=debug", nil, http.StatusBadRequest, "/admin/events", "page"},
		{"/admin/products", nil, http.StatusOK, "/admin/products", "page"},
		{"/admin/products/new", nil, http.StatusOK, "/admin/products", "true"},
		{"/admin/products", badPrice, http.StatusUnprocessableEntity, "/admin/products", "true"},
		{productPath + "/edit", nil, http.StatusOK, "/admin/products", "true"},
		{"/admin/products/" + retired.ID.String() + "/edit", nil, http.StatusNotFound, "/admin/products", "true"},
	}
	for _, tt := range tests {
		assert.Contains(t, requirePage(t, send(mux, tt.path, tt.form), tt.status), navBar(tt.section, tt.current), tt.path)
	}
	failing := adminOver(web.Deps{ProductAdmin: &productAdmin{listErr: errBoom}, EventLog: &eventLog{err: errBoom}})
	for _, path := range []string{"/admin/events", "/admin/products"} {
		assert.Contains(t, requirePage(t, get(failing, path), http.StatusInternalServerError), navBar(path, "true"), path)
	}

	shop, _ := newSite(web.Deps{Products: &catalogue{products: []product.Product{checklist}}})
	for _, path := range []string{"/", "/p/go-payments-checklist", "/p/no-such-product"} {
		assert.NotContains(t, get(shop, path).Body.String(), "<nav", path)
	}
}

// Admin titles are marked, so that a tab or the history tells an admin page from the shop's page
// of the same name.
func TestAdminTitles(t *testing.T) {
	admin := newAdmin(&productAdmin{})
	assert.Contains(t, requirePage(t, get(admin, "/admin/products"), http.StatusOK), "<title>Products · solpay-shop admin</title>")
	assert.Contains(t, requirePage(t, get(admin, "/admin/products/"+retired.ID.String()+"/edit"), http.StatusNotFound),
		"<title>Not found · solpay-shop admin</title>", "an error page too")

	shop, _ := newSite(web.Deps{Products: &catalogue{}})
	assert.Contains(t, requirePage(t, get(shop, "/"), http.StatusOK), "<title>Products · solpay-shop</title>")
}

// send gets path, or posts form to it when form is set.
func send(h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	if form == nil {
		return get(h, path)
	}
	return post(h, path, form)
}

// An admin error page links back to the list of its section; the shop's links to the products
// for sale.
func TestAdminErrorsLinkBackToAdmin(t *testing.T) {
	mux := adminOver(web.Deps{
		ProductAdmin: &productAdmin{products: []product.Product{checklist}},
		OrderAdmin:   &orderAdmin{refundErr: order.ErrNotFound}, EventLog: &eventLog{err: errBoom},
	})
	tooLong := productForm()
	tooLong.Set("description", strings.Repeat("x", 70_000))
	missing := retired.ID.String()
	tests := []struct {
		path   string
		form   url.Values // posted when set
		status int
		back   string
	}{
		{"/admin/products/" + missing + "/edit", nil, http.StatusNotFound, `<a href="/admin/products">All products</a>`},
		{"/admin/products/" + missing, productForm(), http.StatusNotFound, `<a href="/admin/products">All products</a>`},
		{"/admin/products", tooLong, http.StatusBadRequest, `<a href="/admin/products">All products</a>`},
		{"/admin/orders/" + missing, nil, http.StatusNotFound, `<a href="/admin/orders">All orders</a>`},
		{"/admin/orders/" + missing + "/refund", url.Values{}, http.StatusNotFound, `<a href="/admin/orders">All orders</a>`},
		{"/admin/events", nil, http.StatusInternalServerError, `<a href="/admin/events">The event log</a>`},
	}
	for _, tt := range tests {
		body := requirePage(t, send(mux, tt.path, tt.form), tt.status)
		assert.Contains(t, body, "<p>"+tt.back+"</p>", tt.path)
		assert.NotContains(t, body, `<a href="/">`, tt.path)
	}

	shop, _ := newSite(web.Deps{Products: &catalogue{}})
	assert.Contains(t, requirePage(t, get(shop, "/p/no-such-product"), http.StatusNotFound), `<p><a href="/">All products</a></p>`)
}

func TestAdminListsEveryProduct(t *testing.T) {
	a := &productAdmin{products: []product.Product{checklist, retired}}
	body := requirePage(t, get(newAdmin(a), "/admin/products"), http.StatusOK)

	assert.False(t, a.activeOnly, "those no longer for sale too")
	assert.Contains(t, body, `<a class="button" href="/admin/products/new">New product</a>`)
	assert.Contains(t, body, "<tr><td><code>go-payments-checklist</code></td>"+
		"<td><a href=\"/admin/products/"+checklist.ID.String()+"/edit\">Go payments checklist</a></td><td>0.005 SOL</td>"+
		"<td class=\"long\"><code>checklist-v2.md</code></td><td>yes</td></tr>")
	assert.Contains(t, body, "<a href=\"/admin/products/"+retired.ID.String()+"/edit\">Old notes</a></td><td>0.001 SOL</td><td class=\"long\"><code>old.md</code></td><td>no</td></tr>")
}

// The product list scrolls sideways on a narrow screen, as the other lists do, and names every
// column it has.
func TestAdminListScrollsAndNamesItsColumns(t *testing.T) {
	body := requirePage(t, get(newAdmin(&productAdmin{products: []product.Product{checklist}}), "/admin/products"), http.StatusOK)
	assert.Contains(t, body, "<div class=\"table-scroll\">\n<table class=\"list\">")
	assert.Contains(t, body, "<thead><tr><th>Slug</th><th>Title</th><th>Price</th><th>File</th><th>For sale</th></tr></thead>")
	assert.NotContains(t, body, "<th></th>")
}

func TestAdminListWithNoProducts(t *testing.T) {
	assert.Contains(t, requirePage(t, get(newAdmin(&productAdmin{}), "/admin/products"), http.StatusOK),
		`<tr><td colspan="5">No products yet.</td></tr>`)
}

func TestAdminListWhenTheStoreFails(t *testing.T) {
	requirePage(t, get(newAdmin(&productAdmin{listErr: errBoom}), "/admin/products"), http.StatusInternalServerError)
}

func TestAdminNewProductForm(t *testing.T) {
	body := requirePage(t, get(newAdmin(&productAdmin{}), "/admin/products/new"), http.StatusOK)
	assert.Contains(t, body, "<h1>New product</h1>")
	assert.Contains(t, body, `<form class="product-form" method="post" action="/admin/products">`)
	assert.Contains(t, body, `<input id="slug" name="slug" value="" required`)
	assert.Contains(t, body, `<select id="file_name" name="file_name" required>
<option value="">Choose a file</option>
<option>checklist-v2.md</option>
<option>notes.md</option>
</select>`, "no file chosen, and none the product rules refuse")
	assert.Contains(t, body, `<input type="checkbox" name="active" checked>`, "for sale by default")
	assert.NotContains(t, body, `class="problem"`)
}

// productForm is the form for the checklist, as the admin sends it.
func productForm() url.Values {
	return url.Values{
		"slug": {"go-payments-checklist"}, "title": {"Go payments checklist"},
		"description": {"A checklist for payment code in Go."}, "price": {"0.005"},
		"file_name": {"checklist-v2.md"}, "active": {"on"},
	}
}

func TestAdminCreatesAProduct(t *testing.T) {
	a := &productAdmin{}
	w := post(newAdmin(a), "/admin/products", productForm())

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/admin/products", w.Header().Get("Location"))
	want := checklist
	want.ID = uuid.UUID{}
	assert.Equal(t, []product.Product{want}, a.saved)

	form := productForm()
	form.Del("active")
	form.Set("price", " 0.005 ")
	assert.Equal(t, http.StatusSeeOther, post(newAdmin(a), "/admin/products", form).Code)
	require.Len(t, a.saved, 2)
	assert.False(t, a.saved[1].Active, "an unticked box is not for sale")
	assert.Equal(t, int64(5_000_000), a.saved[1].PriceLamports, "spaces around the price are dropped")
}

// The description goes to the product service as the browser sent it, its line breaks as CRLF;
// the service stores them as LF.
func TestAdminSendsTheDescriptionAsSent(t *testing.T) {
	a := &productAdmin{}
	form := productForm()
	form.Set("description", "First line.\r\nSecond line.")
	post(newAdmin(a), "/admin/products", form)
	require.Len(t, a.saved, 1)
	assert.Equal(t, "First line.\r\nSecond line.", a.saved[0].Description)
}

// A price that is not an amount of SOL shows the form again, as sent, without storing anything.
func TestAdminRefusesAPriceThatIsNotSOL(t *testing.T) {
	for _, price := range []string{"", "five", "0.0000000001", "-1", "1e3"} {
		a := &productAdmin{}
		form := productForm()
		form.Set("price", price)
		body := requirePage(t, post(newAdmin(a), "/admin/products", form), http.StatusUnprocessableEntity)

		assert.Contains(t, body, `<p class="problem" role="alert">The price must be an amount of SOL, such as 0.005, with at most 9 decimals.</p>`, price)
		assert.Contains(t, body, "<h1>New product</h1>")
		assert.Contains(t, body, `action="/admin/products">`)
		assert.Contains(t, body, "<option selected>checklist-v2.md</option>", "the file as chosen")
		assert.Contains(t, body, `name="price" value="`+price+`"`, price)
		assert.Contains(t, body, `value="go-payments-checklist"`, "the other fields as sent")
		assert.Empty(t, a.saved, price)
	}
}

// What the product service refuses shows the form again with the problem, the price in SOL.
func TestAdminShowsWhatTheServiceRefuses(t *testing.T) {
	tests := []struct {
		err     error
		status  int
		problem string
	}{
		{&product.ValidationError{Field: "title", Reason: "is required"}, http.StatusUnprocessableEntity, "The title is required."},
		{&product.ValidationError{Field: "price_lamports", Reason: "must be from 1 to 1000000000000 lamports"}, http.StatusUnprocessableEntity,
			"The price must be from 0.000000001 to 1000 SOL."},
		{&product.ValidationError{Field: "file_name", Reason: "is not in the files directory"}, http.StatusUnprocessableEntity,
			"The file is not in the files directory."},
		{product.ErrSlugTaken, http.StatusConflict, "Another product already has this slug."},
	}
	for _, tt := range tests {
		t.Run(tt.problem, func(t *testing.T) {
			body := requirePage(t, post(newAdmin(&productAdmin{err: tt.err}), "/admin/products", productForm()), tt.status)
			assert.Contains(t, body, `<p class="problem" role="alert">`+tt.problem+`</p>`)
			assert.Contains(t, body, `name="price" value="0.005"`)
		})
	}
}

func TestAdminCreateWhenTheStoreFails(t *testing.T) {
	requirePage(t, post(newAdmin(&productAdmin{err: errBoom}), "/admin/products", productForm()), http.StatusInternalServerError)
}

func TestAdminRefusesAFormItCannotRead(t *testing.T) {
	a := &productAdmin{}
	form := productForm()
	form.Set("description", strings.Repeat("x", 70_000))
	requirePage(t, post(newAdmin(a), "/admin/products", form), http.StatusBadRequest)
	assert.Empty(t, a.saved)
}

func TestAdminEditForm(t *testing.T) {
	body := requirePage(t, get(newAdmin(&productAdmin{products: []product.Product{checklist}}), "/admin/products/"+checklist.ID.String()+"/edit"), http.StatusOK)
	assert.Contains(t, body, "<h1>Edit product</h1>")
	assert.Contains(t, body, `action="/admin/products/`+checklist.ID.String()+`"`)
	assert.Contains(t, body, `name="slug" value="go-payments-checklist"`)
	assert.Contains(t, body, `name="title" value="Go payments checklist"`)
	assert.Contains(t, body, `<input type="checkbox" name="active" checked>`)
	assert.Contains(t, body, ">A checklist for payment code in Go.</textarea>")
	assert.Contains(t, body, `name="price" value="0.005"`)
	assert.Contains(t, body, "<option selected>checklist-v2.md</option>")
}

// A product whose file has gone keeps it in the form, so that saving does not pick another.
func TestAdminEditFormKeepsAFileThatHasGone(t *testing.T) {
	p := retired // old.md is not in the files directory
	body := requirePage(t, get(newAdmin(&productAdmin{products: []product.Product{p}}), "/admin/products/"+p.ID.String()+"/edit"), http.StatusOK)
	assert.Contains(t, body, "<option selected>old.md</option>\n<option>checklist-v2.md</option>")
	assert.Contains(t, body, `<input type="checkbox" name="active">`, "not for sale")
}

func TestAdminEditFormWhenTheStoreFails(t *testing.T) {
	a := &productAdmin{products: []product.Product{checklist}, getErr: errBoom}
	requirePage(t, get(newAdmin(a), "/admin/products/"+checklist.ID.String()+"/edit"), http.StatusInternalServerError)
}

func TestAdminEditFormForAProductThatDoesNotExist(t *testing.T) {
	mux := newAdmin(&productAdmin{products: []product.Product{checklist}})
	for _, id := range []string{retired.ID.String(), "not-a-uuid"} {
		assert.Contains(t, requirePage(t, get(mux, "/admin/products/"+id+"/edit"), http.StatusNotFound), "There is no such product.", id)
	}
}

func TestAdminUpdatesAProduct(t *testing.T) {
	a := &productAdmin{products: []product.Product{checklist}}
	form := productForm()
	form.Set("price", "0.007")
	w := post(newAdmin(a), "/admin/products/"+checklist.ID.String(), form)

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/admin/products", w.Header().Get("Location"))
	require.Len(t, a.saved, 1)
	assert.Equal(t, checklist.ID, a.saved[0].ID)
	assert.Equal(t, int64(7_000_000), a.saved[0].PriceLamports)
}

func TestAdminUpdateShowsTheProblemOnTheEditForm(t *testing.T) {
	a := &productAdmin{products: []product.Product{checklist}, err: product.ErrSlugTaken}
	body := requirePage(t, post(newAdmin(a), "/admin/products/"+checklist.ID.String(), productForm()), http.StatusConflict)
	assert.Contains(t, body, "<h1>Edit product</h1>")
	assert.Contains(t, body, `action="/admin/products/`+checklist.ID.String()+`"`)
}

func TestAdminUpdateAProductThatDoesNotExist(t *testing.T) {
	mux := newAdmin(&productAdmin{products: []product.Product{checklist}})
	for _, id := range []string{retired.ID.String(), "not-a-uuid"} {
		requirePage(t, post(mux, "/admin/products/"+id, productForm()), http.StatusNotFound)
	}
}

func TestAdminFormWhenTheFilesCannotBeListed(t *testing.T) {
	mux := adminOver(web.Deps{ProductAdmin: &productAdmin{}, ProductFiles: fileList{err: errors.New("files directory gone")}})
	requirePage(t, get(mux, "/admin/products/new"), http.StatusInternalServerError)
}

// A path under /admin/ that no page has, with any method, gets the error page with its link back
// to the list of the section the path is in, or to the orders from a path in none.
func TestAdminUnknownPagesGetTheErrorPage(t *testing.T) {
	mux := newAdmin(&productAdmin{products: []product.Product{checklist}})
	back := map[string]string{
		"/admin/no-such-page":      `<p><a href="/admin/orders">All orders</a></p>`,
		"/admin/orders/":           `<p><a href="/admin/orders">All orders</a></p>`,
		"/admin/products/new/more": `<p><a href="/admin/products">All products</a></p>`,
	}
	for path, link := range back {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			body := requirePage(t, serve(mux, httptest.NewRequest(method, path, nil)), http.StatusNotFound)
			assert.Contains(t, body, "There is no such page.", method+" "+path)
			assert.Contains(t, body, link, method+" "+path)
			assert.NotContains(t, body, `<a href="/">All products</a>`, method+" "+path)
		}
	}
}

// A wrong method on an admin page's path gets the plain-text 405 that ServeMux sends, with the
// methods the path has, and reaches no handler.
func TestAdminWrongMethodIsNotAllowed(t *testing.T) {
	a := &productAdmin{products: []product.Product{checklist}}
	mux := newAdmin(a)
	tests := []struct{ method, path, allow string }{
		{http.MethodPost, "/admin/", "GET, HEAD"},
		{http.MethodDelete, "/admin/products", "GET, HEAD, POST"},
		{http.MethodPut, "/admin/products/" + checklist.ID.String(), "POST"},
		{http.MethodPost, "/admin/products/" + checklist.ID.String() + "/edit", "GET, HEAD"},
		{http.MethodGet, "/admin/orders/" + checklist.ID.String() + "/refund", "POST"},
	}
	for _, tt := range tests {
		w := serve(mux, httptest.NewRequest(tt.method, tt.path, nil))
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code, tt.path)
		assert.Equal(t, tt.allow, w.Header().Get("Allow"), tt.path)
		assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"), tt.path)
		assert.Equal(t, "Method Not Allowed\n", w.Body.String(), tt.path)
	}
	assert.Empty(t, a.saved, "no product was stored")
}
