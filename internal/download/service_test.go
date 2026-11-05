package download_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/download"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/product"
)

var errBoom = errors.New("boom")

// store holds one order and the product it bought. A set getErr is returned by Get, and markErr
// by MarkFulfilled. It notes how often Get was called and the time of each MarkFulfilled.
type store struct {
	order   order.Order
	product product.Product
	getErr  error
	markErr error
	gets    int
	marks   []time.Time
}

func (s *store) Get(_ context.Context, id uuid.UUID) (order.Order, product.Product, error) {
	s.gets++
	if s.getErr != nil {
		return order.Order{}, product.Product{}, s.getErr
	}
	if id != s.order.ID {
		return order.Order{}, product.Product{}, order.ErrNotFound
	}
	return s.order, s.product, nil
}

func (s *store) MarkFulfilled(_ context.Context, id uuid.UUID, at time.Time) (bool, error) {
	s.marks = append(s.marks, at)
	if s.markErr != nil {
		return false, s.markErr
	}
	if id != s.order.ID || s.order.Status != order.Paid {
		return false, nil
	}
	s.order.Status, s.order.FulfilledAt = order.Fulfilled, &at
	return true, nil
}

// spyFiles opens files from Files and keeps each file it opens. With closed set, it closes a file
// before it returns it.
type spyFiles struct {
	*download.Files
	opened []*os.File
	closed bool
}

func (s *spyFiles) Open(name string) (*os.File, error) {
	f, err := s.Files.Open(name)
	if err == nil {
		s.opened = append(s.opened, f)
		if s.closed {
			_ = f.Close()
		}
	}
	return f, err
}

// modTime is when the product file was last changed.
var modTime = time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)

// rig is a Service over an order in a given status, whose product file is checklist-v2.md.
type rig struct {
	t      *testing.T
	clock  *clock
	secret []byte
	store  *store
	files  *spyFiles
	svc    *download.Service
}

func newRig(t *testing.T, s order.Status) *rig {
	t.Helper()
	dir := t.TempDir()
	name := filepath.Join(dir, "checklist-v2.md")
	require.NoError(t, os.WriteFile(name, []byte("the checklist"), 0o600))
	require.NoError(t, os.Chtimes(name, modTime, modTime))
	files, err := download.OpenFiles(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, files.Close()) })

	r := &rig{t: t, clock: &clock{madeAt}, secret: newSecret(), files: &spyFiles{Files: files}}
	r.store = &store{
		order:   order.Order{ID: orderID, ProductSlug: "go-payments-checklist", Status: s},
		product: product.Product{Slug: "go-payments-checklist", FileName: "checklist-v2.md"},
	}
	r.svc = download.NewService(r.store, r.files, r.secret, r.clock.now)
	return r
}

// link returns the exp and sig of a new link to the order. Unlike Link, it makes one in any
// status.
func (r *rig) link() (exp, sig string) {
	_, exp, sig = split(r.t, download.NewSigner(r.secret, 15*time.Minute, r.clock.now).Path(orderID))
	return exp, sig
}

// open opens the order's file by a new link.
func (r *rig) open(fulfil bool) (download.File, error) {
	exp, sig := r.link()
	return r.svc.Open(r.t.Context(), orderID, exp, sig, fulfil)
}

// read returns what f holds, and closes it.
func read(t *testing.T, f download.File) string {
	t.Helper()
	b, err := io.ReadAll(f.Content)
	require.NoError(t, err)
	require.NoError(t, f.Content.Close())
	return string(b)
}

var statuses = []order.Status{order.Pending, order.Underpaid, order.Paid, order.Fulfilled, order.Expired, order.Refunded}

// Only a paid or fulfilled order gets a link, which works for 15 minutes.
func TestLink(t *testing.T) {
	for _, s := range statuses {
		t.Run(string(s), func(t *testing.T) {
			r := newRig(t, s)
			link, ok := r.svc.Link(r.store.order)
			if s != order.Paid && s != order.Fulfilled {
				assert.False(t, ok)
				assert.Empty(t, link)
				return
			}
			require.True(t, ok)
			id, expires, sig := split(t, link)
			assert.Equal(t, orderID.String(), id)
			assert.Equal(t, strconv.FormatInt(expiry.Unix(), 10), expires)
			r.clock.t = expiry
			f, err := r.svc.Open(t.Context(), orderID, expires, sig, false)
			require.NoError(t, err)
			assert.Equal(t, "the checklist", read(t, f))
		})
	}
}

// The first download of a paid order marks it fulfilled; later ones only download.
func TestOpenFulfilsAPaidOrderOnce(t *testing.T) {
	r := newRig(t, order.Paid)
	f, err := r.open(true)
	require.NoError(t, err)
	assert.Equal(t, "go-payments-checklist.md", f.Name, "the slug, not the stored name")
	assert.True(t, modTime.Equal(f.ModTime), f.ModTime)
	assert.Equal(t, "the checklist", read(t, f))
	assert.Equal(t, []time.Time{madeAt}, r.store.marks)
	assert.Equal(t, order.Fulfilled, r.store.order.Status)

	r.clock.t = madeAt.Add(time.Minute)
	f, err = r.open(true)
	require.NoError(t, err)
	assert.Equal(t, "the checklist", read(t, f))
	assert.Len(t, r.store.marks, 1, "a fulfilled order is not marked again")
}

// Without fulfil, as for a HEAD request, the file opens and the order stays paid.
func TestOpenWithoutFulfilling(t *testing.T) {
	r := newRig(t, order.Paid)
	f, err := r.open(false)
	require.NoError(t, err)
	assert.Equal(t, "the checklist", read(t, f))
	assert.Empty(t, r.store.marks)
	assert.Equal(t, order.Paid, r.store.order.Status)
}

// An order that is not paid, or no longer, downloads nothing, even by a link the shop signed.
func TestOpenRefusesAnOrderThatIsNotPaid(t *testing.T) {
	for _, s := range []order.Status{order.Pending, order.Underpaid, order.Expired, order.Refunded} {
		t.Run(string(s), func(t *testing.T) {
			r := newRig(t, s)
			_, err := r.open(true)
			require.ErrorIs(t, err, download.ErrNotPaid)
			assert.Empty(t, r.files.opened)
			assert.Empty(t, r.store.marks)
		})
	}
}

// A link with a bad signature, or one past its expiry, is refused without reading the store.
func TestOpenChecksTheLinkFirst(t *testing.T) {
	r := newRig(t, order.Paid)
	exp, sig := r.link()
	_, err := r.svc.Open(t.Context(), orderID, exp, flip(sig, 10), true)
	require.ErrorIs(t, err, download.ErrBadSignature)

	r.clock.t = expiry.Add(time.Nanosecond)
	_, err = r.svc.Open(t.Context(), orderID, exp, sig, true)
	require.ErrorIs(t, err, download.ErrLinkExpired)
	assert.Zero(t, r.store.gets)
	assert.Empty(t, r.files.opened)
}

func TestOpenWhenTheStoreFails(t *testing.T) {
	r := newRig(t, order.Paid)
	r.store.getErr = errBoom
	_, err := r.open(true)
	require.ErrorIs(t, err, errBoom)
	assert.Empty(t, r.files.opened)
}

// A signed link to an order the store does not have, as after the database was reset.
func TestOpenAnUnknownOrder(t *testing.T) {
	r := newRig(t, order.Paid)
	r.store.order.ID = uuid.MustParse("6ba7b810-9dad-41d1-80b4-00c04fd430c8")
	_, err := r.open(true)
	require.ErrorIs(t, err, order.ErrNotFound)
}

// A missing file leaves the order paid, so the customer can download once it is back.
func TestOpenWhenTheFileIsMissing(t *testing.T) {
	r := newRig(t, order.Paid)
	r.store.product.FileName = "missing.md"
	_, err := r.open(true)
	require.ErrorIs(t, err, download.ErrFileUnavailable)
	require.ErrorIs(t, err, fs.ErrNotExist, "with the reason")
	require.ErrorContains(t, err, "missing.md")
	assert.Empty(t, r.store.marks)
	assert.Equal(t, order.Paid, r.store.order.Status)
}

func TestOpenWhenTheFileCannotBeRead(t *testing.T) {
	r := newRig(t, order.Paid)
	r.files.closed = true
	_, err := r.open(true)
	require.ErrorIs(t, err, download.ErrFileUnavailable)
	assert.Empty(t, r.store.marks)
}

// If the order cannot be marked fulfilled, the download fails and the file is closed.
func TestOpenWhenFulfillingFails(t *testing.T) {
	r := newRig(t, order.Paid)
	r.store.markErr = errBoom
	_, err := r.open(true)
	require.ErrorIs(t, err, errBoom)
	require.Len(t, r.files.opened, 1)
	assert.ErrorIs(t, r.files.opened[0].Close(), os.ErrClosed, "Open closed the file")
}
