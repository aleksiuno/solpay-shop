package web_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/product"
	"github.com/aleksiuno/solpay-shop/internal/web"
)

// productAdmin holds products. A set err is returned by Create instead of storing, and listErr
// by List. It notes the products it was asked to store and whether List
// was asked for active ones only.
type productAdmin struct {
	products   []product.Product
	err        error
	listErr    error
	saved      []product.Product
	activeOnly bool
}

func (a *productAdmin) List(_ context.Context, activeOnly bool) ([]product.Product, error) {
	a.activeOnly = activeOnly
	return a.products, a.listErr
}

func (a *productAdmin) Create(_ context.Context, p product.Product) (product.Product, error) {
	a.saved = append(a.saved, p)
	return p, a.err
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
		"<td><code>checklist-v2.md</code></td><td>yes</td></tr>")
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

// Line breaks are stored as LF, as the browser counted them, not CRLF, as it sends them.
func TestAdminStoresLineBreaksAsLF(t *testing.T) {
	a := &productAdmin{}
	form := productForm()
	form.Set("description", "First line.\r\nSecond line.")
	post(newAdmin(a), "/admin/products", form)
	require.Len(t, a.saved, 1)
	assert.Equal(t, "First line.\nSecond line.", a.saved[0].Description)
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

func TestAdminFormWhenTheFilesCannotBeListed(t *testing.T) {
	mux := adminOver(web.Deps{ProductAdmin: &productAdmin{}, ProductFiles: fileList{err: errors.New("files directory gone")}})
	requirePage(t, get(mux, "/admin/products/new"), http.StatusInternalServerError)
}
