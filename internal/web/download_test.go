package web_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/download"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/product"
	"github.com/aleksiuno/solpay-shop/internal/web"
)

var (
	orderID = uuid.MustParse("3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e")
	modTime = time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)
	errBoom = errors.New("boom")
)

// content is a file's content that notes whether it was closed.
type content struct {
	*strings.Reader
	closed bool
}

func (c *content) Close() error {
	c.closed = true
	return nil
}

// call is what Downloads.Open was asked.
type call struct {
	id       uuid.UUID
	exp, sig string
	fulfil   bool
}

// downloads opens the checklist, or a file of text if set, or returns err. It notes each call.
type downloads struct {
	err     error
	text    string
	content *content
	calls   []call
}

func (d *downloads) Open(_ context.Context, id uuid.UUID, exp, sig string, fulfil bool) (download.File, error) {
	d.calls = append(d.calls, call{id, exp, sig, fulfil})
	if d.err != nil {
		return download.File{}, d.err
	}
	d.content = &content{Reader: strings.NewReader(cmp.Or(d.text, "the checklist"))}
	return download.File{Name: "go-payments-checklist.md", ModTime: modTime, Content: d.content}, nil
}

// Link gives every order the link below.
func (d *downloads) Link(order.Order) (string, bool) {
	return link, true
}

const link = "/d/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e?exp=1793534400&sig=c2lnbmF0dXJl"

func TestDownloadSendsTheFileAsAnAttachment(t *testing.T) {
	d := &downloads{}
	mux, buf := newSite(web.Deps{Downloads: d})
	w := serve(mux, httptest.NewRequest(http.MethodGet, link, nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "the checklist", w.Body.String())
	assert.Equal(t, "attachment; filename=go-payments-checklist.md", w.Header().Get("Content-Disposition"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "Thu, 01 Oct 2026 09:30:00 GMT", w.Header().Get("Last-Modified"))
	assert.Equal(t, []call{{orderID, "1793534400", "c2lnbmF0dXJl", true}}, d.calls, "a GET fulfils")
	assert.True(t, d.content.closed)
	assert.Empty(t, buf.String())
}

func TestDownloadHeadDoesNotFulfil(t *testing.T) {
	d := &downloads{}
	mux, _ := newSite(web.Deps{Downloads: d})
	w := serve(mux, httptest.NewRequest(http.MethodHead, link, nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, w.Body.String())
	assert.Equal(t, "13", w.Header().Get("Content-Length"))
	require.Len(t, d.calls, 1)
	assert.False(t, d.calls[0].fulfil)
}

func TestDownloadAnswersARange(t *testing.T) {
	mux, _ := newSite(web.Deps{Downloads: &downloads{}})
	r := httptest.NewRequest(http.MethodGet, link, nil)
	r.Header.Set("Range", "bytes=4-8")
	w := serve(mux, r)

	assert.Equal(t, http.StatusPartialContent, w.Code)
	assert.Equal(t, "check", w.Body.String())
}

// A file that takes longer to send than the server's write timeout allows still arrives whole. The
// client reads nothing until that timeout has passed, by which time the file fills the socket's
// buffers many times over.
func TestDownloadOutlivesTheWriteTimeout(t *testing.T) {
	text := strings.Repeat("x", 16<<20)
	mux, _ := newSite(web.Deps{Downloads: &downloads{text: text}})
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.WriteTimeout = 100 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + link)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	time.Sleep(3 * srv.Config.WriteTimeout)
	n, err := io.Copy(io.Discard, resp.Body)
	require.NoError(t, err, "read %d of %d bytes", n, len(text))
	assert.Equal(t, int64(len(text)), n)
}

func TestDownloadRefusals(t *testing.T) {
	missing := fmt.Errorf("%w: open checklist-v2.md: no such file or directory", download.ErrFileUnavailable)
	tests := []struct {
		name   string
		err    error
		status int
		body   string
		logged string // the message of the error line logged, if any
	}{
		{"bad signature", download.ErrBadSignature, http.StatusForbidden, "invalid download link", ""},
		{"not paid", download.ErrNotPaid, http.StatusForbidden, "the order is not paid", ""},
		{"expired", download.ErrLinkExpired, http.StatusGone, "the download link has expired", ""},
		{"no order", order.ErrNotFound, http.StatusNotFound, "no such order", ""},
		{"no file", missing, http.StatusNotFound, "the file is not available", "download file unavailable"},
		{"store failure", errBoom, http.StatusInternalServerError, "internal error", "request failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux, buf := newSite(web.Deps{Downloads: &downloads{err: tt.err}})
			w := serve(mux, httptest.NewRequest(http.MethodGet, link, nil))

			assert.Equal(t, tt.status, w.Code)
			assert.Equal(t, tt.body+"\n", w.Body.String(), "no error text, no file name")
			assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
			assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			assert.Empty(t, w.Header().Get("Content-Disposition"))
			if tt.logged == "" {
				assert.Empty(t, buf.String())
				return
			}
			var line map[string]any
			require.NoError(t, json.Unmarshal(buf.Bytes(), &line), buf.String())
			assert.Equal(t, "ERROR", line["level"])
			assert.Equal(t, tt.logged, line["msg"])
			assert.Equal(t, tt.err.Error(), line["error"])
			if errors.Is(tt.err, download.ErrFileUnavailable) {
				assert.Equal(t, orderID.String(), line["order_id"], "for whoever puts the file back")
			}
		})
	}
}

// A path that is not an order id as the shop writes it cannot be a link the shop signed.
func TestDownloadRefusesAPathThatIsNotAnOrderID(t *testing.T) {
	for _, id := range []string{"not-a-uuid", strings.ToUpper(orderID.String()), "{" + orderID.String() + "}"} {
		d := &downloads{}
		mux, _ := newSite(web.Deps{Downloads: d})
		w := serve(mux, httptest.NewRequest(http.MethodGet, "/d/"+id+"?exp=1793534400&sig=c2lnbmF0dXJl", nil))

		assert.Equal(t, http.StatusForbidden, w.Code, id)
		assert.Equal(t, "invalid download link\n", w.Body.String(), id)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"), id)
		assert.Empty(t, d.calls, id)
	}
}

func TestDownloadWhenTheRequestIsCanceled(t *testing.T) {
	mux, buf := newSite(web.Deps{Downloads: &downloads{err: context.Canceled}})
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errors.New("server: shutdown timed out"))
	w := serve(mux, httptest.NewRequestWithContext(ctx, http.MethodGet, link, nil))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line), buf.String())
	assert.Equal(t, "WARN", line["level"])
	assert.Equal(t, "request canceled", line["msg"])
	assert.Equal(t, "server: shutdown timed out", line["cause"], "the context's cause, not only that it ended")
}

// store holds one order, which MarkFulfilled moves from paid to fulfilled, and its product.
type store struct {
	order order.Order
	marks int
}

func (s *store) Get(_ context.Context, id uuid.UUID) (order.Order, product.Product, error) {
	if id != s.order.ID {
		return order.Order{}, product.Product{}, order.ErrNotFound
	}
	return s.order, product.Product{Slug: "go-payments-checklist", FileName: "checklist-v2.md"}, nil
}

func (s *store) MarkFulfilled(_ context.Context, _ uuid.UUID, _ time.Time) (bool, error) {
	s.marks++
	s.order.Status = order.Fulfilled
	return true, nil
}

// A link that the download service makes works here: the route and its query are the ones the
// service signs.
func TestDownloadByALinkFromTheService(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "checklist-v2.md"), []byte("the checklist"), 0o600))
	files, err := download.OpenFiles(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, files.Close()) })
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	st := &store{order: order.Order{ID: orderID, Status: order.Paid}}
	svc := download.NewService(st, files, []byte(strings.Repeat("test-only-secret-", 2)), func() time.Time { return now })
	mux, _ := newSite(web.Deps{Downloads: svc})

	link, ok := svc.Link(st.order)
	require.True(t, ok)
	w := serve(mux, httptest.NewRequest(http.MethodHead, link, nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Zero(t, st.marks)

	w = serve(mux, httptest.NewRequest(http.MethodGet, link, nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "the checklist", w.Body.String())
	assert.Equal(t, "attachment; filename=go-payments-checklist.md", w.Header().Get("Content-Disposition"))
	assert.Equal(t, 1, st.marks)

	now = now.Add(15*time.Minute + time.Second)
	w = serve(mux, httptest.NewRequest(http.MethodGet, link, nil))
	assert.Equal(t, http.StatusGone, w.Code)
}
