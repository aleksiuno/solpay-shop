package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/product"
)

// linkTTL is how long a download link works.
const linkTTL = 15 * time.Minute

var (
	// ErrNotPaid: the order is not paid, or no longer: it is pending, underpaid, expired or
	// refunded.
	ErrNotPaid = errors.New("download: order not paid")
	// ErrFileUnavailable: the order's file cannot be opened. The error wraps the reason.
	ErrFileUnavailable = errors.New("download: file unavailable")
)

// Store reads orders for downloads and marks them fulfilled.
type Store interface {
	// Get returns the order with id and the product it bought, or order.ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (order.Order, product.Product, error)
	// MarkFulfilled moves the order with id from paid to fulfilled at the time at, with a
	// download.issued event. It returns false, and changes nothing, if the order is not paid.
	MarkFulfilled(ctx context.Context, id uuid.UUID, at time.Time) (bool, error)
}

// Opener opens product files by name. *Files is one.
type Opener interface {
	Open(name string) (*os.File, error)
}

// File is a product file to send.
type File struct {
	Name    string // the name to save it under: the product's slug and the file's extension
	ModTime time.Time
	Content io.ReadSeekCloser // the caller closes it
}

// Service hands out download links for paid orders, and decides whether a link may download.
type Service struct {
	signer Signer
	store  Store
	files  Opener
	now    func() time.Time
}

// NewService returns a Service that reads orders from store and their files from files. Its links
// are signed with secret and work for 15 minutes by the clock now.
func NewService(store Store, files Opener, secret []byte, now func() time.Time) *Service {
	return &Service{signer: NewSigner(secret, linkTTL, now), store: store, files: files, now: now}
}

// Link returns a new download link for o, and true, if o may be downloaded: it is paid or
// fulfilled. A link is a path, as Signer.Path makes it.
func (s *Service) Link(o order.Order) (string, bool) {
	if !downloadable(o.Status) {
		return "", false
	}
	return s.signer.Path(o.ID), true
}

// Open checks a download of the order with id by a link with exp and sig, and opens the order's
// file. It checks, in turn:
//   - the link, before it reads the store: ErrBadSignature, then ErrLinkExpired;
//   - the order, which must be paid or fulfilled: order.ErrNotFound or ErrNotPaid;
//   - the file: ErrFileUnavailable, wrapping the reason.
//
// With fulfil set, a paid order is then marked fulfilled: after the file has opened, so a missing
// file leaves the order paid. If that fails, Open closes the file and returns the error. Set
// fulfil for a download, not for a request that only asks about one, such as HEAD, so that a link
// checker does not fulfil the order.
func (s *Service) Open(ctx context.Context, id uuid.UUID, exp, sig string, fulfil bool) (File, error) {
	if err := s.signer.Check(id, exp, sig); err != nil {
		return File{}, err
	}
	o, p, err := s.store.Get(ctx, id)
	if err != nil {
		return File{}, err
	}
	if !downloadable(o.Status) {
		return File{}, ErrNotPaid
	}
	f, err := s.files.Open(p.FileName)
	if err != nil {
		return File{}, fmt.Errorf("%w: %w", ErrFileUnavailable, err)
	}
	info, err := f.Stat()
	if err != nil {
		return File{}, errors.Join(fmt.Errorf("%w: %w", ErrFileUnavailable, err), f.Close())
	}
	if fulfil && o.Status == order.Paid {
		if _, err := s.store.MarkFulfilled(ctx, id, s.now()); err != nil {
			return File{}, errors.Join(err, f.Close())
		}
	}
	return File{Name: p.Slug + path.Ext(p.FileName), ModTime: info.ModTime(), Content: f}, nil
}

// downloadable reports whether an order in status s may be downloaded.
func downloadable(s order.Status) bool {
	return s == order.Paid || s == order.Fulfilled
}
