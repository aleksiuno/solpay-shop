// Package product holds the catalogue: what a product is and the rules it must meet.
package product

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"
)

// Product is one digital download for sale. An order buys one or more licences of it.
type Product struct {
	ID            uuid.UUID
	Slug          string // the product's name in URLs, such as go-payments-checklist
	Title         string
	Description   string
	PriceLamports int64  // the price of one licence
	FileName      string // the file in the files directory that paid orders download
	Active        bool   // only active products are listed to customers and can be ordered
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

var (
	// ErrNotFound: no product has the slug or id asked for.
	ErrNotFound = errors.New("product: not found")
	// ErrSlugTaken: another product already has the slug.
	ErrSlugTaken = errors.New("product: slug taken")
)

// The limits that Validate checks. The price limit keeps price × quantity within int64 for any
// quantity an order allows, and the schema enforces the same range.
const (
	MaxSlugLength        = 64
	MaxTitleLength       = 120  // in characters
	MaxDescriptionLength = 2000 // in characters
	MaxPriceLamports     = 1_000_000_000_000
)

var (
	slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	// filePattern is the schema's CHECK on file_name: a bare name, with no directory part.
	filePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)
)

// ValidationError says which field of a product is not acceptable, and why. Field is the field's
// name in snake_case, as forms and the API name it.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return "product: invalid " + e.Field + ": " + e.Reason
}

// Validate checks p's fields in order and returns a *ValidationError for the first one that breaks
// a rule. It does not check that the file exists.
func (p Product) Validate() error {
	switch {
	case p.Slug == "":
		return &ValidationError{"slug", "is required"}
	case !ValidSlug(p.Slug):
		return &ValidationError{"slug", "must be the letters a-z and the digits 0-9, in groups joined by single hyphens"}
	case len(p.Slug) > MaxSlugLength: // the slug is ASCII by now, so bytes are characters
		return &ValidationError{"slug", fmt.Sprintf("is longer than %d characters", MaxSlugLength)}
	case strings.TrimSpace(p.Title) == "":
		return &ValidationError{"title", "is required"}
	case !storable(p.Title):
		return &ValidationError{"title", "must be valid UTF-8 text without NUL characters"}
	case utf8.RuneCountInString(p.Title) > MaxTitleLength:
		return &ValidationError{"title", fmt.Sprintf("is longer than %d characters", MaxTitleLength)}
	case !storable(p.Description):
		return &ValidationError{"description", "must be valid UTF-8 text without NUL characters"}
	case utf8.RuneCountInString(p.Description) > MaxDescriptionLength:
		return &ValidationError{"description", fmt.Sprintf("is longer than %d characters", MaxDescriptionLength)}
	case p.PriceLamports < 1 || p.PriceLamports > MaxPriceLamports:
		return &ValidationError{"price_lamports", fmt.Sprintf("must be from 1 to %d lamports", MaxPriceLamports)}
	case p.FileName == "":
		return &ValidationError{"file_name", "is required"}
	case !filePattern.MatchString(p.FileName):
		return &ValidationError{"file_name",
			"must be a bare file name of at most 255 letters, digits, dots, hyphens and underscores, starting with a letter or digit"}
	}
	return nil
}

// ValidSlug reports whether s has the form of a slug: the letters a-z and the digits 0-9, in
// groups joined by single hyphens. It does not check the length.
func ValidSlug(s string) bool {
	return slugPattern.MatchString(s)
}

// storable reports whether s can be stored as Postgres text, which takes only valid UTF-8 and no
// NUL character.
func storable(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
