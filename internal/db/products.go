package db

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aleksiuno/solpay-shop/internal/db/sqlcdb"
	"github.com/aleksiuno/solpay-shop/internal/product"
)

// Products stores the catalogue. It implements product.Repository.
type Products struct {
	q *sqlcdb.Queries
}

var _ product.Repository = (*Products)(nil)

// NewProducts returns a Products that uses pool.
func NewProducts(pool *pgxpool.Pool) *Products {
	return &Products{q: sqlcdb.New(pool)}
}

// List returns every product, or only the active ones, ordered by slug.
func (p *Products) List(ctx context.Context, activeOnly bool) ([]product.Product, error) {
	rows, err := p.q.ListProducts(ctx, activeOnly)
	if err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	out := make([]product.Product, 0, len(rows))
	for _, r := range rows {
		out = append(out, toProduct(r))
	}
	return out, nil
}

// GetBySlug returns the product with slug, or product.ErrNotFound.
func (p *Products) GetBySlug(ctx context.Context, slug string) (product.Product, error) {
	r, err := p.q.GetProductBySlug(ctx, slug)
	if err != nil {
		return product.Product{}, productError("get product", err)
	}
	return toProduct(r), nil
}

// GetByID returns the product with id, or product.ErrNotFound.
func (p *Products) GetByID(ctx context.Context, id uuid.UUID) (product.Product, error) {
	r, err := p.q.GetProductByID(ctx, id)
	if err != nil {
		return product.Product{}, productError("get product", err)
	}
	return toProduct(r), nil
}

// Create stores pr as a new product and returns it with the id and times the database set. A slug
// that another product has gives product.ErrSlugTaken.
func (p *Products) Create(ctx context.Context, pr product.Product) (product.Product, error) {
	r, err := p.q.CreateProduct(ctx, sqlcdb.CreateProductParams{
		Slug:          pr.Slug,
		Title:         pr.Title,
		Description:   pr.Description,
		PriceLamports: pr.PriceLamports,
		FileName:      pr.FileName,
		Active:        pr.Active,
	})
	if err != nil {
		return product.Product{}, productError("create product", err)
	}
	return toProduct(r), nil
}

// Update replaces the editable fields of the product with pr.ID and returns the stored product.
// An unknown id gives product.ErrNotFound, and a slug that another product has gives
// product.ErrSlugTaken.
func (p *Products) Update(ctx context.Context, pr product.Product) (product.Product, error) {
	r, err := p.q.UpdateProduct(ctx, sqlcdb.UpdateProductParams{
		ID:            pr.ID,
		Slug:          pr.Slug,
		Title:         pr.Title,
		Description:   pr.Description,
		PriceLamports: pr.PriceLamports,
		FileName:      pr.FileName,
		Active:        pr.Active,
	})
	if err != nil {
		return product.Product{}, productError("update product", err)
	}
	return toProduct(r), nil
}

// productError turns no row into product.ErrNotFound and a clash on the slug's unique constraint
// into product.ErrSlugTaken, and wraps any other error with what was being done.
func productError(doing string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return product.ErrNotFound
	}
	if pe, ok := errors.AsType[*pgconn.PgError](err); ok && pe.Code == uniqueViolation && pe.ConstraintName == "products_slug_key" {
		return product.ErrSlugTaken
	}
	return fmt.Errorf("%s: %w", doing, err)
}

// uniqueViolation is Postgres's SQLSTATE for a duplicate key.
const uniqueViolation = "23505"

func toProduct(r sqlcdb.Product) product.Product {
	return product.Product{
		ID:            r.ID,
		Slug:          r.Slug,
		Title:         r.Title,
		Description:   r.Description,
		PriceLamports: r.PriceLamports,
		FileName:      r.FileName,
		Active:        r.Active,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
	}
}
