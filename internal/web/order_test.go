package web_test

import (
	"context"
	"html"
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
	"github.com/aleksiuno/solpay-shop/internal/solana/solanatest"
	"github.com/aleksiuno/solpay-shop/internal/web"
)

// orders holds one order, which Create returns and Get finds by its id. A set err is returned by
// every call instead. It notes what Create was asked for, and how often it was called.
type orders struct {
	order   order.Order
	err     error
	slug    string
	qty     int
	creates int
}

func (s *orders) Create(_ context.Context, slug string, qty int) (order.Order, error) {
	s.creates++
	s.slug, s.qty = slug, qty
	if s.err != nil {
		return order.Order{}, s.err
	}
	return s.order, nil
}

func (s *orders) Get(_ context.Context, id uuid.UUID) (order.Order, error) {
	if s.err != nil {
		return order.Order{}, s.err
	}
	if id != s.order.ID {
		return order.Order{}, order.ErrNotFound
	}
	return s.order, nil
}

var (
	merchant  = solanatest.Key("merchant")
	reference = solanatest.Key("reference")
	// deadline is 12:10 UTC, in another zone, as the store may return it.
	deadline = time.Date(2026, 11, 5, 14, 10, 0, 0, time.FixedZone("EET", 2*60*60))
	grace    = 3 * time.Minute // not payment's default, so the page must take it from Deps
	// pending is an order for two licences, due by deadline.
	pending = order.Order{
		ID: orderID, ProductSlug: "go-payments-checklist", ProductTitle: "Go payments checklist",
		Quantity: 2, AmountLamports: 10_000_000, Reference: reference, Status: order.Pending,
		ExpiresAt: deadline, CreatedAt: deadline.Add(-15 * time.Minute),
	}
)

// orderAt returns the order page for o with the clock at now.
func orderAt(t *testing.T, o order.Order, now time.Time) string {
	t.Helper()
	mux, _ := newSite(web.Deps{
		Orders: &orders{order: o}, Downloads: &downloads{}, Merchant: merchant,
		Now: func() time.Time { return now }, ExpiryGrace: grace,
	})
	w := get(mux, "/orders/"+o.ID.String())
	body := requirePage(t, w, http.StatusOK)
	assert.Equal(t, "no-store", w.Result().Header.Get("Cache-Control"))
	return body
}

const refresh = `<meta http-equiv="refresh" content="5">`

// The payment block, with the devnet banner, the wallet link, the address, the amount, the
// reference, the deadline and the devpay command, for a pending order before its deadline.
func TestOrderPageShowsHowToPay(t *testing.T) {
	body := orderAt(t, pending, deadline.Add(-9*time.Minute-10*time.Second))
	u := payment.Request(pending, merchant).URL()

	assert.Contains(t, body, "<h1>Waiting for payment</h1>")
	assert.Contains(t, body, "Go payments checklist, 2 licences, 0.01 SOL")
	assert.Contains(t, body, "<strong>Devnet only.</strong> Switch your wallet to Devnet before paying")
	assert.Contains(t, body, `<a class="button" href="`+html.EscapeString(u)+`">Open in wallet</a>`)
	assert.NotContains(t, body, "ZgotmplZ", "the solana: link is kept")
	assert.Contains(t, body, "<dd>0.01 SOL</dd>")
	assert.Contains(t, body, "<code>"+merchant+"</code>")
	assert.Contains(t, body, "A plain Send to this address is not detected: the payment must include the reference account. Use the wallet link, the QR or devpay.")
	assert.Contains(t, body, "<code>"+reference+"</code>")
	assert.Contains(t, body, "<dd>in 10 min (12:10 UTC)</dd>")
	assert.Contains(t, body, html.EscapeString("go run ./cmd/devpay -keypair data/payer.json '"+u+"'"))
	assert.Contains(t, body, refresh)
	assert.NotContains(t, body, `href="/d/`)
}

// Each state of the order shows its own text, and the page reloads itself only while a payment
// sent on time is still expected.
func TestOrderPageByState(t *testing.T) {
	paidAt := deadline.Add(-5 * time.Minute)
	underpaid := pending
	underpaid.Status, underpaid.ReceivedLamports = order.Underpaid, 4_000_000
	download := `<a class="button" href="` + html.EscapeString(link) + `">Download</a>`
	tests := []struct {
		name    string
		change  func(o *order.Order)
		now     time.Time
		heading string
		texts   []string
		refresh bool
		pay     bool // the payment block is shown
		reload  bool // the link to reload the page is shown
	}{
		{"pending with half a minute left", func(*order.Order) {}, deadline.Add(-30 * time.Second), "Waiting for payment",
			[]string{"<dd>in 1 min (12:10 UTC)</dd>"}, true, true, false},
		{"pending at its deadline", func(*order.Order) {}, deadline, "Waiting for payment",
			[]string{"<dd>now (12:10 UTC)</dd>"}, true, true, false},
		{"underpaid", func(o *order.Order) { *o = underpaid }, deadline.Add(-time.Minute), "Partly paid", []string{
			"Received 0.004 of 0.01 SOL. Pay the remaining 0.006 SOL before the window closes.",
			"<dd>0.006 SOL</dd>", html.EscapeString(payment.Request(underpaid, merchant).URL()),
		}, true, true, false},
		{"pending past its deadline", func(*order.Order) {}, deadline.Add(time.Nanosecond), "Payment window closed",
			[]string{"The payment window closed at 12:10 UTC; confirming any payment sent before then."}, true, false, false},
		{"pending long past its deadline", func(*order.Order) {}, deadline.Add(time.Hour), "Payment window closed",
			[]string{"The payment window closed at 12:10 UTC; confirming any payment sent before then."}, true, false, false},
		{"pending past its deadline with a late payment", func(o *order.Order) { o.HasLatePayment = true }, deadline.Add(time.Minute),
			"Payment window closed", []string{"A payment arrived after the deadline; the merchant will refund it."}, true, false, false},
		{"underpaid past its deadline", func(o *order.Order) { *o = underpaid }, deadline.Add(grace), "Payment window closed",
			[]string{"confirming any payment sent before then"}, true, false, false},
		{"underpaid past the grace", func(o *order.Order) { *o = underpaid }, deadline.Add(grace + time.Nanosecond), "Partly paid",
			[]string{"Partially paid 0.004 of 0.01 SOL. The payment window has closed; the merchant will refund what was received."},
			false, false, true},
		{"underpaid past the grace with a late payment", func(o *order.Order) { *o = underpaid; o.HasLatePayment = true },
			deadline.Add(time.Hour), "Partly paid", []string{"A payment arrived after the deadline; the merchant will refund it."},
			false, false, true},
		{"expired", func(o *order.Order) { o.Status = order.Expired }, deadline.Add(time.Hour), "Expired",
			[]string{"Expired: no payment received."}, false, false, true},
		{"expired with a late payment", func(o *order.Order) { o.Status, o.HasLatePayment = order.Expired, true }, deadline.Add(time.Hour), "Expired",
			[]string{"Expired: a payment arrived after the deadline; the merchant will refund it."}, false, false, true},
		{"paid", func(o *order.Order) { o.Status, o.ReceivedLamports, o.PaidAt = order.Paid, 10_000_000, &paidAt }, deadline.Add(-time.Minute), "Paid",
			[]string{download, "reload this page for a new one"}, false, false, false},
		{"fulfilled", func(o *order.Order) { o.Status, o.PaidAt = order.Fulfilled, &paidAt }, deadline.Add(time.Hour), "Paid",
			[]string{download, "reload this page for a new one"}, false, false, false},
		{"refunded", func(o *order.Order) { o.Status = order.Refunded }, deadline.Add(time.Hour), "Refunded",
			[]string{"Refunded by the merchant."}, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := pending
			tt.change(&o)
			body := orderAt(t, o, tt.now)

			assert.Contains(t, body, "<h1>"+tt.heading+"</h1>")
			for _, text := range tt.texts {
				assert.Contains(t, body, text)
			}
			assert.Equal(t, tt.refresh, strings.Contains(body, refresh), "refresh")
			assert.Equal(t, tt.pay, strings.Contains(body, "Devnet only."), "payment block")
			assert.Equal(t, tt.pay, strings.Contains(body, `href="solana:`), "wallet link")
			assert.Equal(t, tt.pay, strings.Contains(body, `<img class="qr" src="/orders/`+orderID.String()+`/qr.png"`), "QR code")
			assert.Equal(t, o.Status == order.Paid || o.Status == order.Fulfilled, strings.Contains(body, `href="/d/`), "download link")
			assert.Equal(t, tt.reload, strings.Contains(body, `<a href="/orders/`+orderID.String()+`">Reload to check again</a>`), "reload link")
			assert.Equal(t, o.HasLatePayment, strings.Contains(body, "ayment arrived after the deadline"), "late payment")
		})
	}
}

func TestOrderPageCountsLicences(t *testing.T) {
	o := pending
	o.Quantity, o.AmountLamports = 1, 5_000_000
	assert.Contains(t, orderAt(t, o, deadline.Add(-time.Minute)), "Go payments checklist, 1 licence, 0.005 SOL")
}

func TestOrderPageForAnOrderThatDoesNotExist(t *testing.T) {
	o := &orders{order: pending}
	mux, _ := newSite(web.Deps{Orders: o})
	for _, path := range []string{"/orders/6ba7b810-9dad-41d1-80b4-00c04fd430c8", "/orders/not-a-uuid"} {
		body := requirePage(t, get(mux, path), http.StatusNotFound)
		assert.Contains(t, body, "There is no such order.", path)
	}
}

func TestOrderPageWhenTheStoreFails(t *testing.T) {
	mux, buf := newSite(web.Deps{Orders: &orders{err: errBoom}})
	requirePage(t, get(mux, "/orders/"+orderID.String()), http.StatusInternalServerError)
	assert.Equal(t, "ERROR", logLine(t, buf)["level"])
}
