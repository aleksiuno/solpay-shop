package product_test

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/product"
)

// repo keeps products in memory, by id, and refuses a slug that another product has.
type repo struct {
	products map[uuid.UUID]product.Product
	writes   int
}

func newRepo(ps ...product.Product) *repo {
	r := &repo{products: map[uuid.UUID]product.Product{}}
	for _, p := range ps {
		r.products[p.ID] = p
	}
	return r
}

func (r *repo) List(_ context.Context, activeOnly bool) ([]product.Product, error) {
	var out []product.Product
	for _, p := range r.products {
		if p.Active || !activeOnly {
			out = append(out, p)
		}
	}
	return out, nil
}

func (r *repo) GetBySlug(_ context.Context, slug string) (product.Product, error) {
	for _, p := range r.products {
		if p.Slug == slug {
			return p, nil
		}
	}
	return product.Product{}, product.ErrNotFound
}

func (r *repo) GetByID(_ context.Context, id uuid.UUID) (product.Product, error) {
	p, ok := r.products[id]
	if !ok {
		return product.Product{}, product.ErrNotFound
	}
	return p, nil
}

func (r *repo) Create(_ context.Context, p product.Product) (product.Product, error) {
	r.writes++
	if _, err := r.GetBySlug(context.Background(), p.Slug); err == nil {
		return product.Product{}, product.ErrSlugTaken
	}
	p.ID = uuid.NewV4()
	r.products[p.ID] = p
	return p, nil
}

func (r *repo) Update(_ context.Context, p product.Product) (product.Product, error) {
	r.writes++
	if _, ok := r.products[p.ID]; !ok {
		return product.Product{}, product.ErrNotFound
	}
	if other, err := r.GetBySlug(context.Background(), p.Slug); err == nil && other.ID != p.ID {
		return product.Product{}, product.ErrSlugTaken
	}
	r.products[p.ID] = p
	return p, nil
}

// files knows a fixed set of file names, or fails with err.
type files struct {
	names map[string]bool
	err   error
	asked []string
}

func (f *files) Exists(name string) (bool, error) {
	f.asked = append(f.asked, name)
	return f.names[name], f.err
}

func newService() (*product.Service, *repo, *files) {
	r := newRepo()
	f := &files{names: map[string]bool{"go-payments-checklist.md": true, "other.md": true}}
	return product.NewService(r, f), r, f
}

func TestCreate(t *testing.T) {
	s, r, f := newService()
	p := valid()
	p.Title = "  Go payments checklist\n"
	p.Description = "\tA checklist for payment code in Go.  "
	got, err := s.Create(t.Context(), p)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil(), got.ID)
	assert.Equal(t, "Go payments checklist", got.Title, "the title is trimmed")
	assert.Equal(t, "A checklist for payment code in Go.", got.Description, "the description is trimmed")
	assert.Equal(t, got, r.products[got.ID])
	assert.Equal(t, []string{"go-payments-checklist.md"}, f.asked)
}

func TestCreateRefusesAnInvalidProduct(t *testing.T) {
	s, r, f := newService()
	p := valid()
	p.PriceLamports = 0
	_, err := s.Create(t.Context(), p)
	var ve *product.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, "price_lamports", ve.Field)
	assert.Zero(t, r.writes, "nothing is stored")
	assert.Empty(t, f.asked, "the file is checked only for a valid product")

	p = valid()
	p.Title = "   "
	_, err = s.Create(t.Context(), p)
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, "title", ve.Field)
}

func TestCreateNeedsTheFile(t *testing.T) {
	s, r, _ := newService()
	p := valid()
	p.FileName = "missing.md"
	_, err := s.Create(t.Context(), p)
	var ve *product.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, "file_name", ve.Field)
	assert.Zero(t, r.writes)
}

func TestCreateWhenTheFilesCannotBeRead(t *testing.T) {
	r := newRepo()
	broken := errors.New("permission denied")
	s := product.NewService(r, &files{err: broken})
	_, err := s.Create(t.Context(), valid())
	require.ErrorIs(t, err, broken)
	var ve *product.ValidationError
	require.NotErrorAs(t, err, &ve, "a failure to read the files directory is not the product's fault")
	assert.Zero(t, r.writes)
}

func TestCreateWithATakenSlug(t *testing.T) {
	s, _, _ := newService()
	_, err := s.Create(t.Context(), valid())
	require.NoError(t, err)
	_, err = s.Create(t.Context(), valid())
	require.ErrorIs(t, err, product.ErrSlugTaken)
}

func TestUpdate(t *testing.T) {
	s, r, f := newService()
	created, err := s.Create(t.Context(), valid())
	require.NoError(t, err)

	p := created
	p.Title = " New title "
	p.PriceLamports = 7_000_000
	p.FileName = "other.md"
	p.Active = true
	got, err := s.Update(t.Context(), p)
	require.NoError(t, err)
	assert.Equal(t, "New title", got.Title)
	assert.Equal(t, int64(7_000_000), got.PriceLamports)
	assert.Equal(t, got, r.products[created.ID])
	assert.Equal(t, "other.md", f.asked[len(f.asked)-1])

	p.FileName = "missing.md"
	_, err = s.Update(t.Context(), p)
	var ve *product.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, "file_name", ve.Field)
	assert.Equal(t, "other.md", r.products[created.ID].FileName, "a refused update changes nothing")

	p = valid()
	p.ID = uuid.NewV4()
	_, err = s.Update(t.Context(), p)
	require.ErrorIs(t, err, product.ErrNotFound)
}

func TestListAndGet(t *testing.T) {
	active, hidden := valid(), valid()
	active.ID, active.Active = uuid.NewV4(), true
	hidden.ID, hidden.Slug = uuid.NewV4(), "hidden"
	s := product.NewService(newRepo(active, hidden), &files{})

	all, err := s.List(t.Context(), false)
	require.NoError(t, err)
	assert.ElementsMatch(t, []product.Product{active, hidden}, all)
	shown, err := s.List(t.Context(), true)
	require.NoError(t, err)
	assert.Equal(t, []product.Product{active}, shown)

	got, err := s.GetBySlug(t.Context(), "hidden")
	require.NoError(t, err)
	assert.Equal(t, hidden, got)
	got, err = s.GetByID(t.Context(), active.ID)
	require.NoError(t, err)
	assert.Equal(t, active, got)
	_, err = s.GetBySlug(t.Context(), "missing")
	require.ErrorIs(t, err, product.ErrNotFound)
}

// failingRepo fails every call, so a test can tell whether the service asked the store at all.
type failingRepo struct{ product.Repository }

func (failingRepo) GetBySlug(context.Context, string) (product.Product, error) {
	return product.Product{}, errors.New("the store was asked")
}

func TestGetBySlugRefusesWhatCannotBeASlug(t *testing.T) {
	s := product.NewService(failingRepo{}, &files{})
	for _, slug := range []string{"", "\xff", "a\x00b", "Go-Payments", "go_payments", "../etc"} {
		_, err := s.GetBySlug(t.Context(), slug)
		require.ErrorIs(t, err, product.ErrNotFound, "%q", slug)
	}
	_, err := s.GetBySlug(t.Context(), "go-payments-checklist")
	require.EqualError(t, err, "the store was asked", "a valid slug goes to the store")
}
