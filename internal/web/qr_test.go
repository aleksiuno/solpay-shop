package web_test

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
	"github.com/aleksiuno/solpay-shop/internal/web"
)

// qrAt asks for the QR code of o with the clock at now.
func qrAt(o order.Order, now time.Time, err error) *httptest.ResponseRecorder {
	mux, _ := newSite(web.Deps{
		Orders: &orders{order: o, err: err}, Merchant: merchant, Now: func() time.Time { return now },
	})
	return get(mux, "/orders/"+o.ID.String()+"/qr.png")
}

// The code is a PNG of the Solana Pay URL for what remains to pay, with modules of 4 pixels.
func TestOrderQR(t *testing.T) {
	underpaid := pending
	underpaid.Status, underpaid.ReceivedLamports = order.Underpaid, 4_000_000
	for _, o := range []order.Order{pending, underpaid} {
		w := qrAt(o, deadline, nil)
		require.Equal(t, http.StatusOK, w.Code, o.Status)
		sent := w.Result().Header
		assert.Equal(t, "image/png", sent.Get("Content-Type"))
		assert.Equal(t, "nosniff", sent.Get("X-Content-Type-Options"))
		assert.Equal(t, "no-store", sent.Get("Cache-Control"))

		code, err := qrcode.New(payment.Request(o, merchant).URL(), qrcode.Medium)
		require.NoError(t, err)
		want, err := code.PNG(-4)
		require.NoError(t, err)
		assert.Equal(t, want, w.Body.Bytes(), "the code of the URL for the remainder")
		img, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
		require.NoError(t, err)
		modules := len(code.Bitmap()) // with the quiet zone around it
		assert.Equal(t, 4*modules, img.Bounds().Dx())
	}
}

// An order that cannot be paid now has no code.
func TestOrderQRForAnOrderThatCannotBePaid(t *testing.T) {
	paidAt := deadline.Add(-time.Minute)
	tests := []struct {
		name   string
		change func(o *order.Order)
		now    time.Time
	}{
		{"pending past its deadline", func(*order.Order) {}, deadline.Add(time.Nanosecond)},
		{"underpaid past its deadline", func(o *order.Order) { o.Status = order.Underpaid }, deadline.Add(time.Nanosecond)},
		{"paid", func(o *order.Order) { o.Status, o.PaidAt = order.Paid, &paidAt }, deadline.Add(-time.Minute)},
		{"expired", func(o *order.Order) { o.Status = order.Expired }, deadline.Add(-time.Minute)},
		{"refunded", func(o *order.Order) { o.Status = order.Refunded }, deadline.Add(-time.Minute)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := pending
			tt.change(&o)
			requirePage(t, qrAt(o, tt.now, nil), http.StatusNotFound)
		})
	}
}

func TestOrderQRForAnOrderThatDoesNotExist(t *testing.T) {
	mux, _ := newSite(web.Deps{Orders: &orders{order: pending}, Merchant: merchant, Now: time.Now})
	for _, path := range []string{"/orders/6ba7b810-9dad-41d1-80b4-00c04fd430c8/qr.png", "/orders/not-a-uuid/qr.png"} {
		assert.Equal(t, http.StatusNotFound, get(mux, path).Code, path)
	}
}

func TestOrderQRWhenTheStoreFails(t *testing.T) {
	requirePage(t, qrAt(pending, deadline, errBoom), http.StatusInternalServerError)
}
