package web

import (
	"bytes"
	"net/http"
	"time"
	"uuid"

	"github.com/skip2/go-qrcode"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
)

// qrModule is the size of a QR code's modules, its black and white squares, in pixels. A whole
// number keeps every module the same size, however much the URL holds.
const qrModule = 4

// orderQR sends a PNG QR code of the Solana Pay URL of the order whose id is in the path, for a
// wallet to scan, while the order can be paid. Any other order has none: 404.
func (s site) orderQR(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, order.ErrNotFound)
		return
	}
	o, err := s.Orders.Get(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if (o.Status != order.Pending && o.Status != order.Underpaid) || !o.OnTime(s.Now()) {
		s.problem(w, r, http.StatusNotFound, "Not found", "This order cannot be paid now.")
		return
	}
	code, err := qrcode.New(payment.Request(o, s.Merchant).URL(), qrcode.Medium)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	png, err := code.PNG(-qrModule) // a negative size is the size of a module
	if err != nil {
		s.fail(w, r, err)
		return
	}
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	// The code asks for what remains, which a payment changes, and its URL names the order.
	h.Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "qr.png", time.Time{}, bytes.NewReader(png))
}
