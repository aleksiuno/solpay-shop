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
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestAdminListsEveryProduct(t *testing.T) {
	a := &productAdmin{products: []product.Product{checklist, retired}}
	body := requirePage(t, get(newAdmin(a), "/admin/products"), http.StatusOK)

	assert.False(t, a.activeOnly, "those no longer for sale too")
	assert.Contains(t, body, `<a class="button" href="/admin/products/new">New product</a>`)
	assert.Contains(t, body, "<tr><td><code>go-payments-checklist</code></td><td>Go payments checklist</td><td>0.005 SOL</td>"+
		"<td><code>checklist-v2.md</code></td><td>yes</td><td><a href=\"/admin/products/"+checklist.ID.String()+"/edit\">Edit</a></td></tr>")
	assert.Contains(t, body, "<td>Old notes</td><td>0.001 SOL</td><td><code>old.md</code></td><td>no</td>")
}

func TestAdminListWithNoProducts(t *testing.T) {
	assert.Contains(t, requirePage(t, get(newAdmin(&productAdmin{}), "/admin/products"), http.StatusOK), "No products yet.")
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

// A path under /admin/ that no page has, with any method, gets the error page with its link back.
func TestAdminUnknownPagesGetTheErrorPage(t *testing.T) {
	mux := newAdmin(&productAdmin{products: []product.Product{checklist}})
	for _, path := range []string{"/admin/no-such-page", "/admin/orders/", "/admin/products/new/more"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			body := requirePage(t, serve(mux, httptest.NewRequest(method, path, nil)), http.StatusNotFound)
			assert.Contains(t, body, "There is no such page.", method+" "+path)
			assert.Contains(t, body, `<a href="/">All products</a>`, method+" "+path)
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
