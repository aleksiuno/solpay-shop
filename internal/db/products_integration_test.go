//go:build integration

package db_test

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/db"
	"github.com/aleksiuno/solpay-shop/internal/product"
)

// newProduct returns a valid product with a slug that no other test uses.
func newProduct() product.Product {
	return product.Product{
		Slug:          "test-" + strings.ToLower(rand.Text()),
		Title:         "Test product",
		Description:   "For a test.",
		PriceLamports: 2_000_000,
		FileName:      "test.md",
		Active:        true,
	}
}

// slugs returns the slugs of ps, in order.
func slugs(ps []product.Product) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Slug)
	}
	return out
}

func TestSeedProductsLoad(t *testing.T) {
	seed, err := os.ReadFile("../../scripts/seed.sql")
	require.NoError(t, err)
	for range 2 { // the seed is safe to run more than once
		_, err = pool.Exec(t.Context(), string(seed))
		require.NoError(t, err)
	}

	products := db.NewProducts(pool)
	notes, err := products.GetBySlug(t.Context(), "solana-pay-field-notes")
	require.NoError(t, err)
	assert.Equal(t, "Solana Pay field notes", notes.Title)
	assert.Equal(t, int64(1_000_000), notes.PriceLamports)
	assert.Equal(t, "solana-pay-field-notes.md", notes.FileName)
	assert.True(t, notes.Active)

	checklist, err := products.GetBySlug(t.Context(), "go-payments-checklist")
	require.NoError(t, err)
	assert.Equal(t, int64(5_000_000), checklist.PriceLamports)

	active, err := products.List(t.Context(), true)
	require.NoError(t, err)
	assert.Subset(t, slugs(active), []string{"go-payments-checklist", "solana-pay-field-notes"})
	for _, p := range []product.Product{notes, checklist} {
		require.NoError(t, p.Validate(), p.Slug)
		require.FileExists(t, filepath.Join("../../testdata/files", p.FileName), "the file of %s", p.Slug)
	}
}

func TestProductsCreateAndGet(t *testing.T) {
	products := db.NewProducts(pool)
	want := newProduct()
	before := time.Now()
	got, err := products.Create(t.Context(), want)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil(), got.ID)
	assert.WithinDuration(t, before, got.CreatedAt, time.Minute)
	assert.True(t, got.UpdatedAt.Equal(got.CreatedAt))
	want.ID, want.CreatedAt, want.UpdatedAt = got.ID, got.CreatedAt, got.UpdatedAt
	assert.Equal(t, want, got)

	bySlug, err := products.GetBySlug(t.Context(), want.Slug)
	require.NoError(t, err)
	assert.Equal(t, got.ID, bySlug.ID)
	byID, err := products.GetByID(t.Context(), got.ID)
	require.NoError(t, err)
	assert.Equal(t, want.Slug, byID.Slug)
	assert.True(t, byID.CreatedAt.Equal(got.CreatedAt))

	_, err = products.GetBySlug(t.Context(), "missing-"+strings.ToLower(rand.Text()))
	require.ErrorIs(t, err, product.ErrNotFound)
	_, err = products.GetByID(t.Context(), uuid.NewV4())
	require.ErrorIs(t, err, product.ErrNotFound)
}

func TestProductsSlugTaken(t *testing.T) {
	products := db.NewProducts(pool)
	first, err := products.Create(t.Context(), newProduct())
	require.NoError(t, err)

	again := newProduct()
	again.Slug = first.Slug
	_, err = products.Create(t.Context(), again)
	require.ErrorIs(t, err, product.ErrSlugTaken)

	other, err := products.Create(t.Context(), newProduct())
	require.NoError(t, err)
	other.Slug = first.Slug
	_, err = products.Update(t.Context(), other)
	require.ErrorIs(t, err, product.ErrSlugTaken)
}

func TestProductsUpdate(t *testing.T) {
	products := db.NewProducts(pool)
	hidden := newProduct()
	hidden.Active = false
	created, err := products.Create(t.Context(), hidden)
	require.NoError(t, err)

	p := created
	p.Slug = "renamed-" + strings.ToLower(rand.Text())
	p.Title, p.Description = "New title", "New description"
	p.PriceLamports, p.FileName, p.Active = 9_000_000, "other.md", true
	got, err := products.Update(t.Context(), p)
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, p.Slug, got.Slug)
	assert.Equal(t, "New title", got.Title)
	assert.Equal(t, "New description", got.Description)
	assert.Equal(t, int64(9_000_000), got.PriceLamports)
	assert.Equal(t, "other.md", got.FileName)
	assert.True(t, got.Active, "a hidden product can be shown again")
	assert.True(t, got.CreatedAt.Equal(created.CreatedAt), "the creation time stays")
	assert.True(t, got.UpdatedAt.After(created.UpdatedAt), "the update time moves")

	p.ID = uuid.NewV4()
	p.Slug = "missing-" + strings.ToLower(rand.Text())
	_, err = products.Update(t.Context(), p)
	require.ErrorIs(t, err, product.ErrNotFound)
}

func TestProductsList(t *testing.T) {
	products := db.NewProducts(pool)
	shown, hidden := newProduct(), newProduct()
	hidden.Active = false
	_, err := products.Create(t.Context(), shown)
	require.NoError(t, err)
	_, err = products.Create(t.Context(), hidden)
	require.NoError(t, err)

	active, err := products.List(t.Context(), true)
	require.NoError(t, err)
	assert.Contains(t, slugs(active), shown.Slug)
	assert.NotContains(t, slugs(active), hidden.Slug)
	for _, p := range active {
		assert.True(t, p.Active, p.Slug)
	}

	all, err := products.List(t.Context(), false)
	require.NoError(t, err)
	assert.Subset(t, slugs(all), []string{shown.Slug, hidden.Slug})
	assert.True(t, slices.IsSorted(slugs(all)), "ordered by slug")
}

func TestProductsRefusesWhatTheSchemaForbids(t *testing.T) {
	p := newProduct()
	p.PriceLamports = 0
	_, err := db.NewProducts(pool).Create(t.Context(), p)
	require.ErrorContains(t, err, "create product")
	require.NotErrorIs(t, err, product.ErrSlugTaken)
}
