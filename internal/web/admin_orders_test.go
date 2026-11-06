package web_test

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
	"github.com/aleksiuno/solpay-shop/internal/web"
)

// orderAdmin holds orders. A set err is returned by every call instead, and refundErr by
// Refund. It notes the filter List was asked with, how often, and the refunds asked for.
type orderAdmin struct {
	orders    []order.Order
	err       error
	refundErr error
	filter    order.ListFilter
	lists     int
	refunds   []string // the id and the note of each refund, joined by a space
}

func (a *orderAdmin) Refund(_ context.Context, id uuid.UUID, note string) (order.Order, error) {
	a.refunds = append(a.refunds, id.String()+" "+note)
	if a.refundErr != nil {
		return order.Order{}, a.refundErr
	}
	return order.Order{ID: id, Status: order.Refunded}, nil
}

func (a *orderAdmin) List(_ context.Context, f order.ListFilter) ([]order.Order, error) {
	a.lists++
	a.filter = f
	return a.orders, a.err
}

func (a *orderAdmin) Get(_ context.Context, id uuid.UUID) (order.Order, error) {
	if a.err != nil {
		return order.Order{}, a.err
	}
	for _, o := range a.orders {
		if o.ID == id {
			return o, nil
		}
	}
	return order.Order{}, order.ErrNotFound
}

// ledger holds an order's ledger rows, or fails with err.
type ledger struct {
	payments []payment.Payment
	err      error
}

func (l ledger) Ledger(context.Context, uuid.UUID) ([]payment.Payment, error) {
	return l.payments, l.err
}

// eventLog holds events, or fails with err. It notes the filter List was asked with.
type eventLog struct {
	events []audit.Event
	err    error
	filter audit.ListFilter
}

func (l *eventLog) List(_ context.Context, f audit.ListFilter) ([]audit.Event, error) {
	l.filter = f
	return l.events, l.err
}

// adminOrders returns the admin pages over a, l and e, with the clock an hour after the
// deadline.
func adminOrders(a *orderAdmin, l ledger, e *eventLog) *http.ServeMux {
	now := deadline.Add(time.Hour)
	return adminOver(web.Deps{OrderAdmin: a, Ledger: l, EventLog: e, Now: func() time.Time { return now }})
}

func TestAdminRootGoesToTheOrders(t *testing.T) {
	w := get(adminOrders(&orderAdmin{}, ledger{}, &eventLog{}), "/admin/")
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/admin/orders", w.Header().Get("Location"))
}

// The list shows each order with the flags that call for a look.
func TestAdminOrderList(t *testing.T) {
	late, overpaid, short, shortOnTime := pending, pending, pending, pending
	late.ID, late.Status, late.HasLatePayment = uuid.MustParse("11111111-1111-4111-8111-111111111111"), order.Expired, true
	overpaid.ID, overpaid.Status, overpaid.ReceivedLamports = uuid.MustParse("22222222-2222-4222-8222-222222222222"), order.Paid, 12_000_000
	short.ID, short.Status, short.ReceivedLamports = uuid.MustParse("33333333-3333-4333-8333-333333333333"), order.Underpaid, 4_000_000
	// shortOnTime's deadline is the clock's time: the window is not closed yet.
	shortOnTime.ID, shortOnTime.Status, shortOnTime.ExpiresAt = uuid.MustParse("44444444-4444-4444-8444-444444444444"), order.Underpaid, deadline.Add(time.Hour)
	a := &orderAdmin{orders: []order.Order{late, overpaid, short, shortOnTime}}
	body := requirePage(t, get(adminOrders(a, ledger{}, &eventLog{}), "/admin/orders"), http.StatusOK)

	assert.Equal(t, order.ListFilter{}, a.filter, "every status")
	assert.Contains(t, body, `<option value="">All</option>`)
	assert.Contains(t, body, `<tr><td class="time"><a href="/admin/orders/`+late.ID.String()+`">2026-11-05 11:55:00 UTC</a></td>`+
		`<td>Go payments checklist</td><td>2</td><td>0.01 SOL</td><td>0 SOL</td><td>expired</td><td><span class="flag">late payment</span></td></tr>`)
	assert.Contains(t, body, `<td>0.012 SOL</td><td>paid</td><td><span class="flag">overpaid</span></td>`)
	assert.Contains(t, body, `<td>0.004 SOL</td><td>underpaid</td><td><span class="flag">underpaid</span>, <span class="flag">window closed</span></td>`)
	assert.Contains(t, body, `<td>0 SOL</td><td>underpaid</td><td><span class="flag">underpaid</span></td></tr>`, "not past the deadline")
	assert.Equal(t, 1, strings.Count(body, "window closed"))
	assert.Contains(t, body, `<select id="status" name="status">
<option value="">All</option>
<option>pending</option>
<option>underpaid</option>
<option>paid</option>
<option>fulfilled</option>
<option>expired</option>
<option>refunded</option>
</select>`)
}

// The list reads the clock once, so its rows are flagged at the same instant even when the
// deadline passes while it is being built.
func TestAdminOrderListFlagsEveryRowAtOneInstant(t *testing.T) {
	first, second := pending, pending
	first.ID, first.Status = uuid.MustParse("11111111-1111-4111-8111-111111111111"), order.Underpaid
	second.ID, second.Status = uuid.MustParse("22222222-2222-4222-8222-222222222222"), order.Underpaid
	reads := 0
	clock := func() time.Time {
		reads++
		if reads == 1 {
			return deadline
		}
		return deadline.Add(time.Hour)
	}
	mux := adminOver(web.Deps{OrderAdmin: &orderAdmin{orders: []order.Order{first, second}}, Ledger: ledger{}, EventLog: &eventLog{}, Now: clock})
	body := requirePage(t, get(mux, "/admin/orders"), http.StatusOK)
	assert.Equal(t, 2, strings.Count(body, `<span class="flag">underpaid</span></td></tr>`))
	assert.NotContains(t, body, "window closed")
}

func TestAdminOrderListByStatus(t *testing.T) {
	for _, s := range order.Statuses() {
		a := &orderAdmin{}
		body := requirePage(t, get(adminOrders(a, ledger{}, &eventLog{}), "/admin/orders?status="+string(s)), http.StatusOK)
		assert.Equal(t, order.ListFilter{Status: s}, a.filter)
		assert.Contains(t, body, "<option selected>"+string(s)+"</option>")
		assert.Contains(t, body, "No orders.")
	}
}

func TestAdminOrderListRefusesAnUnknownStatus(t *testing.T) {
	a := &orderAdmin{}
	body := requirePage(t, get(adminOrders(a, ledger{}, &eventLog{}), "/admin/orders?status=lost"), http.StatusBadRequest)
	assert.Contains(t, body, "There is no such status.")
	assert.Zero(t, a.lists)
}

func TestAdminOrderListWhenTheStoreFails(t *testing.T) {
	requirePage(t, get(adminOrders(&orderAdmin{err: errBoom}, ledger{}, &eventLog{}), "/admin/orders"), http.StatusInternalServerError)
}

// One order's page shows its fields, its ledger rows with explorer links, and its events.
func TestAdminOrderPage(t *testing.T) {
	o := pending
	paidAt, refundedAt := deadline.Add(-5*time.Minute), deadline.Add(time.Hour)
	o.Status, o.ReceivedLamports, o.PaidAt, o.RefundedAt = order.Refunded, 10_000_000, &paidAt, &refundedAt
	blockTime := deadline.Add(-6 * time.Minute)
	l := ledger{payments: []payment.Payment{
		{Signature: "sig-credited", OrderID: o.ID, Outcome: payment.Credited, Lamports: 10_000_000, Slot: 7, BlockTime: &blockTime},
		{Signature: "sig-failed", OrderID: o.ID, Outcome: payment.Rejected, Slot: 9, Reason: payment.TxFailed},
	}}
	e := &eventLog{events: []audit.Event{
		{ID: 3, OrderID: &o.ID, Kind: audit.OrderRefunded, Message: "order refunded",
			Data: map[string]any{"note": "Q&A <said so>"}, CreatedAt: deadline.Add(time.Hour)},
		{ID: 2, OrderID: &o.ID, Kind: audit.PaymentLate, Message: "payment after the deadline", Signature: "sig-late",
			Data: map[string]any{"lamports": json.Number("2000")}, CreatedAt: deadline.Add(time.Minute)},
		{ID: 1, OrderID: &o.ID, Kind: audit.OrderCreated, Message: "order created", CreatedAt: deadline.Add(-15 * time.Minute)},
	}}
	body := requirePage(t, get(adminOrders(&orderAdmin{orders: []order.Order{o}}, l, e), "/admin/orders/"+o.ID.String()), http.StatusOK)

	assert.Equal(t, audit.ListFilter{OrderID: &o.ID}, e.filter, "the order's events")
	for _, want := range []string{
		`<code>` + o.ID.String() + `</code> (<a href="/orders/` + o.ID.String() + `">the customer's page</a>)`,
		"<dd>Go payments checklist (<code>go-payments-checklist</code>), 2 licences</dd>",
		"<dt>Status</dt><dd>refunded</dd>", "<dd>0.01 SOL</dd>", "<code>" + reference + "</code>",
		"<dt>Deadline</dt><dd>2026-11-05 12:10:00 UTC</dd>", "<dt>Paid</dt><dd>2026-11-05 12:05:00 UTC</dd>",
		"<dt>Refunded</dt><dd>2026-11-05 13:10:00 UTC</dd>",
		`<td class="long"><a href="https://explorer.solana.com/tx/sig-credited?cluster=devnet"><code>sig-credited</code></a></td><td>credited</td><td>0.01 SOL</td><td>7</td><td class="time">2026-11-05 12:04:00 UTC</td>`,
		"<td>rejected: tx_failed</td><td>0 SOL</td><td>9</td><td class=\"time\">unknown</td>",
		`<tr class="lvl-warn"><td class="time">2026-11-05 12:11:00 UTC</td><td>warn</td><td><code>payment.late</code></td><td>payment after the deadline</td>` +
			`<td class="long"><code>sig-late</code> <code>` + html.EscapeString(`{"lamports":2000}`) + `</code></td></tr>`,
		`<tr class="lvl-info"><td class="time">2026-11-05 11:55:00 UTC</td><td>info</td><td><code>order.created</code></td><td>order created</td><td class="long"></td></tr>`,
		"<code>" + html.EscapeString(`{"note":"Q&A <said so>"}`) + "</code>",
	} {
		assert.Contains(t, body, want)
	}
	assert.NotContains(t, body, "<dt>Downloaded</dt>")
	assert.NotContains(t, body, "<dt>Flags</dt>")
}

// The flags of an order have a row of their own.
func TestAdminOrderPageShowsTheFlags(t *testing.T) {
	o := pending
	o.Status, o.ReceivedLamports, o.HasLatePayment = order.Underpaid, 4_000_000, true
	body := requirePage(t, get(adminOrders(&orderAdmin{orders: []order.Order{o}}, ledger{}, &eventLog{}), "/admin/orders/"+o.ID.String()), http.StatusOK)
	assert.Contains(t, body, "<dt>Status</dt><dd>underpaid</dd>")
	assert.Contains(t, body, "<dt>Received</dt><dd>0.004 SOL</dd>")
	assert.Contains(t, body, `<dt>Flags</dt><dd><span class="flag">late payment</span>, <span class="flag">underpaid</span>, <span class="flag">window closed</span></dd>`)
}

func TestAdminOrderPageWithNothingSeen(t *testing.T) {
	body := requirePage(t, get(adminOrders(&orderAdmin{orders: []order.Order{pending}}, ledger{}, &eventLog{}), "/admin/orders/"+pending.ID.String()), http.StatusOK)
	assert.Contains(t, body, "No payments seen.")
	assert.Contains(t, body, "No events.")
}

func TestAdminOrderPageForAnOrderThatDoesNotExist(t *testing.T) {
	mux := adminOrders(&orderAdmin{orders: []order.Order{pending}}, ledger{}, &eventLog{})
	for _, id := range []string{"6ba7b810-9dad-41d1-80b4-00c04fd430c8", "not-a-uuid"} {
		assert.Contains(t, requirePage(t, get(mux, "/admin/orders/"+id), http.StatusNotFound), "There is no such order.", id)
	}
}

func TestAdminOrderPageWhenTheStoresFail(t *testing.T) {
	a := &orderAdmin{orders: []order.Order{pending}}
	path := "/admin/orders/" + pending.ID.String()
	requirePage(t, get(adminOrders(&orderAdmin{err: errBoom}, ledger{}, &eventLog{}), path), http.StatusInternalServerError)
	requirePage(t, get(adminOrders(a, ledger{err: errBoom}, &eventLog{}), path), http.StatusInternalServerError)
	requirePage(t, get(adminOrders(a, ledger{}, &eventLog{err: errBoom}), path), http.StatusInternalServerError)
}

// paidOrder is pending, paid.
func paidOrder() order.Order {
	o := pending
	paidAt := deadline.Add(-time.Minute)
	o.Status, o.ReceivedLamports, o.PaidAt = order.Paid, 10_000_000, &paidAt
	return o
}

func TestAdminRefundsAnOrder(t *testing.T) {
	o := paidOrder()
	a := &orderAdmin{orders: []order.Order{o}}
	w := post(adminOrders(a, ledger{}, &eventLog{}), "/admin/orders/"+o.ID.String()+"/refund", url.Values{"note": {" Sent back on request. "}})

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/admin/orders/"+o.ID.String(), w.Header().Get("Location"))
	assert.Equal(t, []string{o.ID.String() + "  Sent back on request. "}, a.refunds, "the note as sent; the service trims it")
}

// A refund the order service refuses shows the order's page again with the reason, and the note
// as typed.
func TestAdminRefundShowsWhyItIsRefused(t *testing.T) {
	tests := []struct {
		err     error
		status  int
		problem string
	}{
		{order.ErrNoLatePayment, http.StatusConflict,
			"An expired order can be refunded only after a payment arrived after its deadline, and none has."},
		{fmt.Errorf("%w: refunded to refunded", order.ErrInvalidTransition), http.StatusConflict,
			"This order cannot be refunded: it is refunded already, or it has received nothing."},
		{order.ErrStaleState, http.StatusConflict, "The order changed while it was being refunded; look at it again."},
		{&order.ValidationError{Field: "note", Reason: "is longer than 500 characters"}, http.StatusUnprocessableEntity,
			"The note is longer than 500 characters."},
	}
	for _, tt := range tests {
		t.Run(tt.problem, func(t *testing.T) {
			o := paidOrder()
			a := &orderAdmin{orders: []order.Order{o}, refundErr: tt.err}
			w := post(adminOrders(a, ledger{}, &eventLog{}), "/admin/orders/"+o.ID.String()+"/refund", url.Values{"note": {"by hand"}})

			body := requirePage(t, w, tt.status)
			assert.Contains(t, body, `<p class="problem" role="alert">`+tt.problem+`</p>`)
			assert.Contains(t, body, `>by hand</textarea>`)
			assert.Contains(t, body, "<dd>paid</dd>", "the order's page")
		})
	}
}

func TestAdminRefundAnOrderThatDoesNotExist(t *testing.T) {
	a := &orderAdmin{orders: []order.Order{pending}, refundErr: order.ErrNotFound}
	mux := adminOrders(a, ledger{}, &eventLog{})
	for _, id := range []string{"6ba7b810-9dad-41d1-80b4-00c04fd430c8", "not-a-uuid"} {
		requirePage(t, post(mux, "/admin/orders/"+id+"/refund", url.Values{}), http.StatusNotFound)
	}
	assert.Len(t, a.refunds, 1, "a malformed id is not asked for")
}

func TestAdminRefundWhenTheStoreFails(t *testing.T) {
	o := paidOrder()
	a := &orderAdmin{orders: []order.Order{o}, refundErr: errBoom}
	requirePage(t, post(adminOrders(a, ledger{}, &eventLog{}), "/admin/orders/"+o.ID.String()+"/refund", url.Values{}), http.StatusInternalServerError)
}

func TestAdminRefundRefusesAFormItCannotRead(t *testing.T) {
	o := paidOrder()
	a := &orderAdmin{orders: []order.Order{o}}
	form := url.Values{"note": {strings.Repeat("x", 9000)}}
	requirePage(t, post(adminOrders(a, ledger{}, &eventLog{}), "/admin/orders/"+o.ID.String()+"/refund", form), http.StatusBadRequest)
	assert.Empty(t, a.refunds)
}

// The refund form is offered where the order service would refund the order: once it has
// received a payment, on time or, for an expired one, late.
func TestAdminOrderPageOffersARefund(t *testing.T) {
	refunded, expired, late, underpaid, fulfilled := paidOrder(), pending, pending, pending, paidOrder()
	refunded.Status, expired.Status, underpaid.Status, fulfilled.Status = order.Refunded, order.Expired, order.Underpaid, order.Fulfilled
	late.Status, late.HasLatePayment = order.Expired, true
	for _, tt := range []struct {
		o     order.Order
		offer bool
	}{
		{paidOrder(), true}, {underpaid, true}, {fulfilled, true}, {late, true},
		{expired, false}, {pending, false}, {refunded, false},
	} {
		body := requirePage(t, get(adminOrders(&orderAdmin{orders: []order.Order{tt.o}}, ledger{}, &eventLog{}), "/admin/orders/"+tt.o.ID.String()), http.StatusOK)
		assert.Equal(t, tt.offer, strings.Contains(body, `<form class="product-form" method="post" action="/admin/orders/`+tt.o.ID.String()+`/refund">`), tt.o.Status)
	}
}
