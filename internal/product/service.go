package product

import (
	"context"
	"fmt"
	"strings"
	"uuid"
)

// Repository stores products.
type Repository interface {
	List(ctx context.Context, activeOnly bool) ([]Product, error)
	GetBySlug(ctx context.Context, slug string) (Product, error) // ErrNotFound
	GetByID(ctx context.Context, id uuid.UUID) (Product, error)  // ErrNotFound
	// Create stores a new product, with an id and times set by the store. ErrSlugTaken.
	Create(ctx context.Context, p Product) (Product, error)
	// Update replaces the editable fields of the product with p.ID. ErrNotFound, ErrSlugTaken.
	Update(ctx context.Context, p Product) (Product, error)
}

// FileChecker reports whether a product file exists.
type FileChecker interface {
	Exists(name string) (bool, error)
}

// Service lists products and creates and updates them after checking their fields.
type Service struct {
	repo  Repository
	files FileChecker
}

// NewService returns a Service that stores products in repo and checks their files with files.
func NewService(repo Repository, files FileChecker) *Service {
	return &Service{repo: repo, files: files}
}

// List returns every product, or only the active ones.
func (s *Service) List(ctx context.Context, activeOnly bool) ([]Product, error) {
	return s.repo.List(ctx, activeOnly)
}

// GetBySlug returns the product with slug, or ErrNotFound. A string that is not in the form of a
// slug, such as one with a byte that is not UTF-8, gives ErrNotFound without asking the store, so
// a visitor's URL cannot make the lookup fail.
func (s *Service) GetBySlug(ctx context.Context, slug string) (Product, error) {
	if !ValidSlug(slug) {
		return Product{}, ErrNotFound
	}
	return s.repo.GetBySlug(ctx, slug)
}

// GetByID returns the product with id, or ErrNotFound.
func (s *Service) GetByID(ctx context.Context, id uuid.UUID) (Product, error) {
	return s.repo.GetByID(ctx, id)
}

// Create checks p and stores it as a new product. It trims spaces around the title and description
// first. A field that breaks a rule, or a file that does not exist, gives a *ValidationError;
// a slug that another product has gives ErrSlugTaken.
func (s *Service) Create(ctx context.Context, p Product) (Product, error) {
	p, err := s.check(p)
	if err != nil {
		return Product{}, err
	}
	return s.repo.Create(ctx, p)
}

// Update checks p as Create does and stores it over the product with p.ID. An unknown id gives
// ErrNotFound.
func (s *Service) Update(ctx context.Context, p Product) (Product, error) {
	p, err := s.check(p)
	if err != nil {
		return Product{}, err
	}
	return s.repo.Update(ctx, p)
}

// check trims p's title and description, validates p and checks that its file exists.
func (s *Service) check(p Product) (Product, error) {
	p.Title = strings.TrimSpace(p.Title)
	p.Description = strings.TrimSpace(p.Description)
	if err := p.Validate(); err != nil {
		return Product{}, err
	}
	ok, err := s.files.Exists(p.FileName)
	if err != nil {
		return Product{}, fmt.Errorf("product: check file %s: %w", p.FileName, err)
	}
	if !ok {
		return Product{}, &ValidationError{"file_name", "no such file in the files directory"}
	}
	return p, nil
}
