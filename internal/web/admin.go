package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/money"
	"github.com/aleksiuno/solpay-shop/internal/product"
)

// ProductAdmin lists, finds, creates and updates products for the admin pages. *product.Service
// is one.
type ProductAdmin interface {
	List(ctx context.Context, activeOnly bool) ([]product.Product, error)
	GetByID(ctx context.Context, id uuid.UUID) (product.Product, error)
	Create(ctx context.Context, p product.Product) (product.Product, error)
	Update(ctx context.Context, p product.Product) (product.Product, error)
}

// FileLister lists the files that a product can sell. *download.Files is one.
type FileLister interface {
	List() ([]string, error)
}

// maxProductForm is the largest product form body that the admin pages read.
const maxProductForm = 64 << 10

// RegisterAdmin adds the admin pages to mux, which the caller guards with BasicAuth. A path under
// /admin/ that no page has gets the error page, as under /.
func RegisterAdmin(mux *http.ServeMux, d Deps) {
	s := site{Deps: d}
	mux.Handle("GET /admin/{$}", http.RedirectHandler("/admin/orders", http.StatusFound))
	mux.HandleFunc("GET /admin/orders", s.adminOrders)
	mux.HandleFunc("GET /admin/orders/{id}", s.adminOrderPage)
	mux.HandleFunc("POST /admin/orders/{id}/refund", s.refundOrder)
	mux.HandleFunc("GET /admin/events", s.adminEvents)
	mux.HandleFunc("GET /admin/products", s.adminProducts)
	mux.HandleFunc("GET /admin/products/new", s.newProduct)
	mux.HandleFunc("POST /admin/products", s.createProduct)
	mux.HandleFunc("GET /admin/products/{id}/edit", s.editProduct)
	mux.HandleFunc("POST /admin/products/{id}", s.updateProduct)
	mux.Handle("/admin/", s.unknown(mux, "/admin/"))
}

// productForm is what the product form shows: the fields as they stand, the files to choose
// from, where the form posts, and what was wrong with the last one sent.
type productForm struct {
	Heading     string
	Action      string
	Slug        string
	Title       string
	Description string
	Price       string // in SOL, as typed
	FileName    string
	Active      bool
	Files       []string
	Problem     string
}

// adminProducts lists every product, those no longer for sale included.
func (s site) adminProducts(w http.ResponseWriter, r *http.Request) {
	ps, err := s.ProductAdmin.List(r.Context(), false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "admin_products.html", view{Title: "Products", Data: ps})
}

// newProduct shows an empty form for a new product, for sale by default.
func (s site) newProduct(w http.ResponseWriter, r *http.Request) {
	s.showProductForm(w, r, http.StatusOK, productForm{Heading: "New product", Action: "/admin/products", Active: true})
}

// editProduct shows the form for the product whose id is in the path, filled in.
func (s site) editProduct(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, product.ErrNotFound)
		return
	}
	p, err := s.ProductAdmin.GetByID(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.showProductForm(w, r, http.StatusOK, productForm{
		Heading: "Edit product", Action: "/admin/products/" + p.ID.String(),
		Slug: p.Slug, Title: p.Title, Description: p.Description, Price: money.FormatSOL(p.PriceLamports),
		FileName: p.FileName, Active: p.Active,
	})
}

// createProduct stores a new product from the form and sends the browser back to the list with a
// 303. A field that is not acceptable shows the form again with the problem, 422; a slug that
// another product has, 409.
func (s site) createProduct(w http.ResponseWriter, r *http.Request) {
	s.saveProduct(w, r, productForm{Heading: "New product", Action: "/admin/products"}, s.ProductAdmin.Create)
}

// updateProduct stores the form over the product whose id is in the path, as createProduct does.
func (s site) updateProduct(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, product.ErrNotFound)
		return
	}
	form := productForm{Heading: "Edit product", Action: "/admin/products/" + id.String()}
	s.saveProduct(w, r, form, func(ctx context.Context, p product.Product) (product.Product, error) {
		p.ID = id
		return s.ProductAdmin.Update(ctx, p)
	})
}

// saveProduct reads the product form into form, stores it with save, and answers as
// createProduct says.
func (s site) saveProduct(w http.ResponseWriter, r *http.Request, form productForm, save func(context.Context, product.Product) (product.Product, error)) {
	r.Body = http.MaxBytesReader(w, r.Body, maxProductForm)
	if err := r.ParseForm(); err != nil {
		s.problem(w, r, http.StatusBadRequest, "Bad request", "The form could not be read.")
		return
	}
	f := r.PostForm
	form.Slug, form.Title, form.Description = f.Get("slug"), f.Get("title"), f.Get("description")
	form.Price, form.FileName, form.Active = f.Get("price"), f.Get("file_name"), f.Has("active")
	lamports, err := money.ParseSOL(strings.TrimSpace(form.Price))
	if err != nil {
		form.Problem = "The price must be an amount of SOL, such as 0.005, with at most 9 decimals."
		s.showProductForm(w, r, http.StatusUnprocessableEntity, form)
		return
	}
	_, err = save(r.Context(), product.Product{
		Slug: form.Slug, Title: form.Title, Description: form.Description, PriceLamports: lamports,
		FileName: form.FileName, Active: form.Active,
	})
	var invalid *product.ValidationError
	switch {
	case errors.As(err, &invalid):
		form.Problem = fieldProblem(invalid)
		s.showProductForm(w, r, http.StatusUnprocessableEntity, form)
	case errors.Is(err, product.ErrSlugTaken):
		form.Problem = "Another product already has this slug."
		s.showProductForm(w, r, http.StatusConflict, form)
	case err != nil:
		s.fail(w, r, err)
	default:
		http.Redirect(w, r, "/admin/products", http.StatusSeeOther)
	}
}

// fieldProblem says what is wrong with a field of the product form, as the form names it. The
// price is in SOL there, not in lamports.
func fieldProblem(e *product.ValidationError) string {
	switch e.Field {
	case "price_lamports":
		return fmt.Sprintf("The price must be from %s to %s SOL.", money.FormatSOL(1), money.FormatSOL(product.MaxPriceLamports))
	case "file_name":
		return "The file " + e.Reason + "."
	}
	return "The " + e.Field + " " + e.Reason + "."
}

// showProductForm writes the product form with status. The files to choose from are those in the
// files directory whose names the product rules accept, and the form's own file if it is not
// among them, so that the form never picks another one in its place.
func (s site) showProductForm(w http.ResponseWriter, r *http.Request, status int, form productForm) {
	files, err := s.ProductFiles.List()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	files = slices.DeleteFunc(files, func(name string) bool { return !product.ValidFileName(name) })
	if form.FileName != "" && !slices.Contains(files, form.FileName) {
		files = append([]string{form.FileName}, files...)
	}
	form.Files = files
	s.render(w, r, status, "admin_product_form.html", view{Title: form.Heading, Data: form})
}
