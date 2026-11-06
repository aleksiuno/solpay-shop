package web

import (
	"context"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"time"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/download"
	"github.com/aleksiuno/solpay-shop/internal/order"
)

// downloadTimeout bounds the time a download has to send its file. The server's write timeout
// suits a page, but would cut a large file off on a slow link.
const downloadTimeout = 10 * time.Minute

// download sends the file of the order in the path, by a link that download.Service.Link made.
// GET downloads the file, and the first GET of a paid order marks it fulfilled; HEAD only checks
// the link and the file. http.ServeContent answers Range and conditional requests.
//
// A refusal is plain text: 403 for a link the shop did not sign or an order that is not paid, 410
// for an expired link, and 404 for an order or a file that is not there. Nothing in an answer
// names the files directory or the name the file is stored under. No cache may keep an answer,
// since the link is the customer's key to the file.
func (s site) download(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// The shop signs only order ids in their canonical form, as uuid.UUID.String writes them.
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil || id.String() != r.PathValue("id") {
		http.Error(w, "invalid download link", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	f, err := s.Downloads.Open(r.Context(), id, q.Get("exp"), q.Get("sig"), r.Method == http.MethodGet)
	if err != nil {
		s.refuse(w, r, id, err)
		return
	}
	defer f.Content.Close()
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
	// The deadline is real time, not the shop's clock. Setting it fails only for a writer that
	// takes no deadline, such as a test's recorder.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(downloadTimeout))
	http.ServeContent(w, r, f.Name, f.ModTime, f.Content)
}

// refuse answers err, which Downloads.Open returned for the order with id. A file that cannot be
// opened, and an error it does not know, are logged; the answer never carries the error.
func (s site) refuse(w http.ResponseWriter, r *http.Request, id uuid.UUID, err error) {
	ctx := r.Context()
	switch {
	case errors.Is(err, download.ErrBadSignature):
		http.Error(w, "invalid download link", http.StatusForbidden)
	case errors.Is(err, download.ErrNotPaid):
		http.Error(w, "the order is not paid", http.StatusForbidden)
	case errors.Is(err, download.ErrLinkExpired):
		http.Error(w, "the download link has expired", http.StatusGone)
	case errors.Is(err, order.ErrNotFound):
		http.Error(w, "no such order", http.StatusNotFound)
	case errors.Is(err, download.ErrFileUnavailable):
		s.Log.LogAttrs(ctx, slog.LevelError, "download file unavailable",
			slog.String("order_id", id.String()), slog.Any("error", err))
		http.Error(w, "the file is not available", http.StatusNotFound)
	case ctx.Err() != nil:
		// The client hung up, or the server is stopping; no one reads the answer.
		s.Log.LogAttrs(ctx, slog.LevelWarn, "request canceled",
			slog.Any("error", err), slog.Any("cause", context.Cause(ctx)))
		http.Error(w, "request canceled", http.StatusInternalServerError)
	default:
		s.Log.LogAttrs(ctx, slog.LevelError, "request failed", slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
