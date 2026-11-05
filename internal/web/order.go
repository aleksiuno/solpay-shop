package web

import (
	"fmt"
	"html/template"
	"math"
	"net/http"
	"time"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/money"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
)

// orderPage is what the order page shows. Pay is set while the order takes payments, and Download
// once it is paid.
type orderPage struct {
	Order    order.Order
	Heading  string
	Notes    []string
	Pay      *payBlock
	Download string
	Reload   bool // offer to reload the page, which does not reload itself
}

// payBlock says how to pay what remains of an order.
type payBlock struct {
	URL       template.URL // the Solana Pay URL, which html/template would otherwise refuse in href
	Recipient string
	Reference string
	Amount    string // in SOL
	Closes    string
	Devpay    string // the devpay command that pays the URL
}

// order shows the order whose id is in the path: how to pay it while it can be paid, and its
// download link once it is paid. While a payment sent on time is still expected, the page reloads
// itself every 5 seconds. An id that is not a UUID names no order.
func (s site) order(w http.ResponseWriter, r *http.Request) {
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
	page, refresh := s.orderPage(o, s.Now())
	// The page's URL is the customer's only key to the order, so no cache may keep it.
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "order.html", view{Title: "Order", Refresh: refresh, Data: page})
}

// orderPage returns the page for o at now, and whether it should reload itself.
//
// A pending order past its deadline keeps reloading until the watcher expires it, however long
// that takes, as during an RPC outage. An underpaid one never expires, so its page stops
// reloading ExpiryGrace after the deadline, by when a payment sent on time has normally been
// found. The watcher still credits one it finds later, within its late window, so the page offers
// a reload.
func (s site) orderPage(o order.Order, now time.Time) (orderPage, bool) {
	p := orderPage{Order: o}
	closed := now.After(o.ExpiresAt)
	switch o.Status {
	case order.Pending, order.Underpaid:
		switch {
		case !closed:
			if o.Status == order.Pending {
				p.Heading = "Waiting for payment"
			} else {
				p.Heading = "Partly paid"
				p.Notes = []string{fmt.Sprintf("Received %s of %s SOL. Pay the remaining %s SOL before the window closes.",
					money.FormatSOL(o.ReceivedLamports), money.FormatSOL(o.AmountLamports), money.FormatSOL(o.RemainingLamports()))}
			}
			p.Pay = s.payBlock(o, now)
			return p, true
		case o.Status == order.Pending || !now.After(o.ExpiresAt.Add(s.ExpiryGrace)):
			p.Heading = "Payment window closed"
			p.Notes = []string{fmt.Sprintf("The payment window closed at %s; confirming any payment sent before then.", clock(o.ExpiresAt))}
			p.Notes = append(p.Notes, lateNote(o)...)
			return p, true
		default:
			p.Heading = "Partly paid"
			p.Notes = []string{fmt.Sprintf("Partially paid %s of %s SOL. The payment window has closed; the merchant will refund what was received.",
				money.FormatSOL(o.ReceivedLamports), money.FormatSOL(o.AmountLamports))}
			p.Notes = append(p.Notes, lateNote(o)...)
			p.Reload = true
		}
	case order.Expired:
		p.Heading = "Expired"
		p.Notes = []string{"Expired: no payment received."}
		if o.HasLatePayment {
			p.Notes = []string{"Expired: a payment arrived after the deadline; the merchant will refund it."}
		}
		p.Reload = true
	case order.Paid, order.Fulfilled:
		p.Heading = "Paid"
		p.Notes = []string{"Thank you. The download link works for 15 minutes; reload this page for a new one."}
		p.Download, _ = s.Downloads.Link(o)
	case order.Refunded:
		p.Heading = "Refunded"
		p.Notes = []string{"Refunded by the merchant."}
	}
	return p, false
}

// payBlock returns how to pay what remains of o at now, which is not past its deadline.
func (s site) payBlock(o order.Order, now time.Time) *payBlock {
	u := payment.Request(o, s.Merchant).URL()
	when := "now"
	if minutes := int(math.Ceil(o.ExpiresAt.Sub(now).Minutes())); minutes > 0 {
		when = fmt.Sprintf("in %d min", minutes)
	}
	return &payBlock{
		URL:       template.URL(u), //nolint:gosec // a solana: URL from solanapay, which query-escapes every value, the product title included
		Recipient: s.Merchant,
		Reference: o.Reference,
		Amount:    money.FormatSOL(o.RemainingLamports()),
		Closes:    when + " (" + clock(o.ExpiresAt) + ")",
		Devpay:    "go run ./cmd/devpay -keypair data/payer.json '" + u + "'",
	}
}

// lateNote says, for an order that a payment reached after its deadline, that the merchant will
// refund it: a late payment is never credited.
func lateNote(o order.Order) []string {
	if !o.HasLatePayment {
		return nil
	}
	return []string{"A payment arrived after the deadline; the merchant will refund it."}
}

// clock returns the time of day of t in UTC, to the minute.
func clock(t time.Time) string {
	return t.UTC().Format("15:04 UTC")
}
