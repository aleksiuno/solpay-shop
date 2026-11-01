package product_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/product"
)

// valid returns a product that passes Validate.
func valid() product.Product {
	return product.Product{
		Slug:          "go-payments-checklist",
		Title:         "Go payments checklist",
		Description:   "A checklist for payment code in Go.",
		PriceLamports: 5_000_000,
		FileName:      "go-payments-checklist.md",
	}
}

func TestValidateAccepts(t *testing.T) {
	tests := map[string]func(*product.Product){
		"as is":                 func(*product.Product) {},
		"one-character slug":    func(p *product.Product) { p.Slug = "a" },
		"digits in the slug":    func(p *product.Product) { p.Slug = "v2-notes-2026" },
		"64-character slug":     func(p *product.Product) { p.Slug = strings.Repeat("a", 64) },
		"120-character title":   func(p *product.Product) { p.Title = strings.Repeat("é", 120) },
		"no description":        func(p *product.Product) { p.Description = "" },
		"2000-char description": func(p *product.Product) { p.Description = strings.Repeat("ü", 2000) },
		"lowest price":          func(p *product.Product) { p.PriceLamports = 1 },
		"highest price":         func(p *product.Product) { p.PriceLamports = 1_000_000_000_000 },
		"file name with dots":   func(p *product.Product) { p.FileName = "notes.v2.tar.gz" },
		"file name of 255":      func(p *product.Product) { p.FileName = strings.Repeat("f", 255) },
	}
	for name, change := range tests {
		p := valid()
		change(&p)
		assert.NoError(t, p.Validate(), name)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		change func(*product.Product)
		field  string
	}{
		{"no slug", func(p *product.Product) { p.Slug = "" }, "slug"},
		{"65-character slug", func(p *product.Product) { p.Slug = strings.Repeat("a", 65) }, "slug"},
		{"upper case", func(p *product.Product) { p.Slug = "Go-payments" }, "slug"},
		{"leading hyphen", func(p *product.Product) { p.Slug = "-go" }, "slug"},
		{"trailing hyphen", func(p *product.Product) { p.Slug = "go-" }, "slug"},
		{"double hyphen", func(p *product.Product) { p.Slug = "go--payments" }, "slug"},
		{"underscore", func(p *product.Product) { p.Slug = "go_payments" }, "slug"},
		{"space", func(p *product.Product) { p.Slug = "go payments" }, "slug"},
		{"slash", func(p *product.Product) { p.Slug = "go/payments" }, "slug"},
		{"non-ASCII letter", func(p *product.Product) { p.Slug = "café" }, "slug"},
		{"no title", func(p *product.Product) { p.Title = "" }, "title"},
		{"title of spaces", func(p *product.Product) { p.Title = " \t " }, "title"},
		{"121-character title", func(p *product.Product) { p.Title = strings.Repeat("é", 121) }, "title"},
		{"NUL in the title", func(p *product.Product) { p.Title = "a\x00b" }, "title"},
		{"invalid UTF-8 in the title", func(p *product.Product) { p.Title = "a\xffb" }, "title"},
		{"NUL in the description", func(p *product.Product) { p.Description = "a\x00b" }, "description"},
		{"invalid UTF-8 in the description", func(p *product.Product) { p.Description = "a\xffb" }, "description"},
		{"2001-char description", func(p *product.Product) { p.Description = strings.Repeat("ü", 2001) }, "description"},
		{"free", func(p *product.Product) { p.PriceLamports = 0 }, "price_lamports"},
		{"negative price", func(p *product.Product) { p.PriceLamports = -1 }, "price_lamports"},
		{"price above the limit", func(p *product.Product) { p.PriceLamports = 1_000_000_000_001 }, "price_lamports"},
		{"no file", func(p *product.Product) { p.FileName = "" }, "file_name"},
		{"file in a directory", func(p *product.Product) { p.FileName = "sub/notes.md" }, "file_name"},
		{"parent directory", func(p *product.Product) { p.FileName = "../notes.md" }, "file_name"},
		{"absolute path", func(p *product.Product) { p.FileName = "/etc/passwd" }, "file_name"},
		{"dot file", func(p *product.Product) { p.FileName = ".env" }, "file_name"},
		{"space in the file name", func(p *product.Product) { p.FileName = "my notes.md" }, "file_name"},
		{"file name of 256", func(p *product.Product) { p.FileName = strings.Repeat("f", 256) }, "file_name"},
	}
	for _, tt := range tests {
		p := valid()
		tt.change(&p)
		var ve *product.ValidationError
		require.ErrorAs(t, p.Validate(), &ve, tt.name)
		assert.Equal(t, tt.field, ve.Field, tt.name)
		assert.NotEmpty(t, ve.Reason, tt.name)
	}
}

func TestValidateSlugReasons(t *testing.T) {
	tests := map[string]string{
		"café":                     "product: invalid slug: must be the letters a-z and the digits 0-9, in groups joined by single hyphens",
		strings.Repeat("é", 40):    "product: invalid slug: must be the letters a-z and the digits 0-9, in groups joined by single hyphens",
		strings.Repeat("a", 65):    "product: invalid slug: is longer than 64 characters",
		"go-payments\n":            "product: invalid slug: must be the letters a-z and the digits 0-9, in groups joined by single hyphens",
		"go-payments\x00checklist": "product: invalid slug: must be the letters a-z and the digits 0-9, in groups joined by single hyphens",
	}
	for slug, want := range tests {
		p := valid()
		p.Slug = slug
		require.EqualError(t, p.Validate(), want, "%q", slug)
	}
}

func TestValidateFileNameReason(t *testing.T) {
	for _, name := range []string{"_draft.md", "-notes.md", "my notes.md"} {
		p := valid()
		p.FileName = name
		require.EqualError(t, p.Validate(), "product: invalid file_name: must be a bare file name of at most 255 "+
			"letters, digits, dots, hyphens and underscores, starting with a letter or digit", name)
	}
}

func TestValidateReportsTheFirstField(t *testing.T) {
	err := product.Product{PriceLamports: 0, FileName: ""}.Validate()
	require.EqualError(t, err, "product: invalid slug: is required")
}
