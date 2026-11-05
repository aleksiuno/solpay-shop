package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/httpapi"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
)

// orderAdmin holds orders. A set err is returned by every call instead, and refundErr by Refund.
// It notes the filter List was asked with and the notes of the refunds asked for.
type orderAdmin struct {
	orders    []order.Order
	err       error
	refundErr error
	filter    order.ListFilter
	notes     []string
}

func (a *orderAdmin) List(_ context.Context, f order.ListFilter) ([]order.Order, error) {
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

func (a *orderAdmin) Refund(ctx context.Context, id uuid.UUID, note string) (order.Order, error) {
	a.notes = append(a.notes, note)
	if a.refundErr != nil {
		return order.Order{}, a.refundErr
	}
	o, err := a.Get(ctx, id)
	if err != nil {
		return order.Order{}, err
	}
	at := now.Add(time.Hour).In(eet)
	o.Status, o.RefundedAt = order.Refunded, &at
	return o, nil
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

// newOrderAdminAPI returns the admin routes over a, l and e.
func newOrderAdminAPI(a *orderAdmin, l ledger, e *eventLog) *http.ServeMux {
	log, _ := newLogger()
	mux := http.NewServeMux()
	httpapi.RegisterAdmin(mux, httpapi.Deps{OrderAdmin: a, Ledger: l, EventLog: e, Log: log})
	return mux
}

// eet is a zone other than UTC, as the store may return times in.
var eet = time.FixedZone("EET", 2*60*60)

// paidOrder is pending, paid a minute before its deadline.
func paidOrder() order.Order {
	o := pending
	paidAt := now.Add(14 * time.Minute).In(eet)
	o.Status, o.ReceivedLamports, o.PaidAt = order.Paid, 10_000_000, &paidAt
	return o
}

// paidJSON is paidOrder as the admin API shows it.
const paidJSON = `{"id": "3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e", "status": "paid",
	"product": {"slug": "go-payments-checklist", "title": "Go payments checklist"},
	"quantity": 2, "amount_lamports": 10000000, "received_lamports": 10000000,
	"reference": "` + "REF" + `", "has_late_payment": false,
	"expires_at": "2026-11-03T12:15:00Z", "created_at": "2026-11-03T12:00:00Z",
	"paid_at": "2026-11-03T12:14:00Z", "fulfilled_at": null, "refunded_at": null}`

// withRef puts the order's reference into a JSON text that holds REF.
func withRef(s string) string {
	b, _ := json.Marshal(reference)
	return strings.ReplaceAll(s, `"REF"`, string(b))
}

func TestAdminListsOrders(t *testing.T) {
	late := pending
	late.ID, late.Status, late.HasLatePayment = uuid.MustParse("11111111-1111-4111-8111-111111111111"), order.Expired, true
	a := &orderAdmin{orders: []order.Order{paidOrder(), late}}
	w := get(newOrderAdminAPI(a, ledger{}, &eventLog{}), "/api/v1/admin/orders?status=paid")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, order.ListFilter{Status: order.Paid}, a.filter)
	lateJSON := `{"id": "11111111-1111-4111-8111-111111111111", "status": "expired",
		"product": {"slug": "go-payments-checklist", "title": "Go payments checklist"},
		"quantity": 2, "amount_lamports": 10000000, "received_lamports": 0, "reference": "REF",
		"has_late_payment": true, "expires_at": "2026-11-03T12:15:00Z", "created_at": "2026-11-03T12:00:00Z",
		"paid_at": null, "fulfilled_at": null, "refunded_at": null}`
	assert.JSONEq(t, `{"orders": [`+withRef(paidJSON)+`, `+withRef(lateJSON)+`]}`, w.Body.String())

	for _, s := range []order.Status{order.Pending, order.Underpaid, order.Fulfilled, order.Expired, order.Refunded} {
		a := &orderAdmin{}
		assert.Equal(t, http.StatusOK, get(newOrderAdminAPI(a, ledger{}, &eventLog{}), "/api/v1/admin/orders?status="+string(s)).Code)
		assert.Equal(t, order.ListFilter{Status: s}, a.filter)
	}

	a = &orderAdmin{}
	w = get(newOrderAdminAPI(a, ledger{}, &eventLog{}), "/api/v1/admin/orders")
	assert.JSONEq(t, `{"orders": []}`, w.Body.String())
	assert.Equal(t, order.ListFilter{}, a.filter, "every status")
}

func TestAdminListOrdersRefusesAnUnknownStatus(t *testing.T) {
	w := get(newOrderAdminAPI(&orderAdmin{}, ledger{}, &eventLog{}), "/api/v1/admin/orders?status=lost")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	code, msg := envelope(t, w.Body)
	assert.Equal(t, "invalid_request", code)
	assert.Equal(t, "status must be one of pending, underpaid, paid, fulfilled, expired, refunded", msg)
}

// One order comes with its ledger rows and its events.
func TestAdminGetsAnOrder(t *testing.T) {
	o := paidOrder()
	blockTime := now.Add(13 * time.Minute).In(time.FixedZone("EET", 2*60*60))
	l := ledger{payments: []payment.Payment{
		{Signature: "sig-credited", OrderID: o.ID, Outcome: payment.Credited, Lamports: 10_000_000, Slot: 7, BlockTime: &blockTime},
		{Signature: "sig-failed", OrderID: o.ID, Outcome: payment.Rejected, Slot: 9, Reason: payment.TxFailed},
	}}
	e := &eventLog{events: []audit.Event{{
		ID: 5, OrderID: &o.ID, Kind: audit.PaymentLate, Message: "payment after the deadline", Signature: "sig-late",
		CreatedAt: now.Add(16 * time.Minute).In(eet),
	}, {
		ID: 4, OrderID: &o.ID, Kind: audit.PaymentVerified, Message: "order paid", Signature: "sig-credited",
		Data: map[string]any{"lamports": json.Number("10000000")}, CreatedAt: now.Add(14 * time.Minute).In(eet),
	}}}
	w := get(newOrderAdminAPI(&orderAdmin{orders: []order.Order{o}}, l, e), "/api/v1/admin/orders/"+o.ID.String())

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, audit.ListFilter{OrderID: &o.ID}, e.filter)
	full := withRef(paidJSON)
	full = full[:len(full)-1] + `, "payments": [
		{"signature": "sig-credited", "outcome": "credited", "lamports": 10000000, "slot": 7, "block_time": "2026-11-03T12:13:00Z"},
		{"signature": "sig-failed", "outcome": "rejected", "reason": "tx_failed", "lamports": 0, "slot": 9, "block_time": null}],
		"events": [{"id": 5, "order_id": "3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e", "level": "warn", "kind": "payment.late",
		"message": "payment after the deadline", "signature": "sig-late", "data": null, "created_at": "2026-11-03T12:16:00Z"},
		{"id": 4, "order_id": "3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e", "level": "info", "kind": "payment.verified",
		"message": "order paid", "signature": "sig-credited", "data": {"lamports": 10000000}, "created_at": "2026-11-03T12:14:00Z"}]}`
	assert.JSONEq(t, full, w.Body.String())
}

func TestAdminGetsAnOrderWithNothingSeen(t *testing.T) {
	w := get(newOrderAdminAPI(&orderAdmin{orders: []order.Order{pending}}, ledger{}, &eventLog{}), "/api/v1/admin/orders/"+pending.ID.String())
	assert.Contains(t, w.Body.String(), `"payments":[],"events":[]`)
}

func TestAdminGetOrderFailures(t *testing.T) {
	a := &orderAdmin{orders: []order.Order{pending}}
	tests := []struct {
		name   string
		mux    *http.ServeMux
		path   string
		status int
	}{
		{"unknown", newOrderAdminAPI(a, ledger{}, &eventLog{}), "/api/v1/admin/orders/6ba7b810-9dad-41d1-80b4-00c04fd430c8", http.StatusNotFound},
		{"not a UUID", newOrderAdminAPI(a, ledger{}, &eventLog{}), "/api/v1/admin/orders/x", http.StatusNotFound},
		{"order store", newOrderAdminAPI(&orderAdmin{err: errBoom}, ledger{}, &eventLog{}), "/api/v1/admin/orders/" + pending.ID.String(), http.StatusInternalServerError},
		{"ledger", newOrderAdminAPI(a, ledger{err: errBoom}, &eventLog{}), "/api/v1/admin/orders/" + pending.ID.String(), http.StatusInternalServerError},
		{"events", newOrderAdminAPI(a, ledger{}, &eventLog{err: errBoom}), "/api/v1/admin/orders/" + pending.ID.String(), http.StatusInternalServerError},
		{"list", newOrderAdminAPI(&orderAdmin{err: errBoom}, ledger{}, &eventLog{}), "/api/v1/admin/orders", http.StatusInternalServerError},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.status, get(tt.mux, tt.path).Code, tt.name)
	}
}

func TestAdminRefundsAnOrder(t *testing.T) {
	a := &orderAdmin{orders: []order.Order{paidOrder()}}
	w := send(newOrderAdminAPI(a, ledger{}, &eventLog{}), http.MethodPost,
		"/api/v1/admin/orders/"+pending.ID.String()+"/refund", `{"note": "sent back"}`)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"refunded"`)
	assert.Contains(t, w.Body.String(), `"refunded_at":"2026-11-03T13:00:00Z"`)
	assert.Equal(t, []string{"sent back"}, a.notes)
}

// A refund the order service refuses, and a body that is not acceptable, get their envelopes.
func TestAdminRefundRefusals(t *testing.T) {
	tests := []struct {
		name, body string
		err        error
		status     int
		code, msg  string
	}{
		{"no late payment", `{}`, order.ErrNoLatePayment, http.StatusConflict, "invalid_transition",
			"an expired order can be refunded only after a late payment"},
		{"wrong status", `{}`, fmt.Errorf("%w: pending to refunded", order.ErrInvalidTransition), http.StatusConflict,
			"invalid_transition", "the order's status does not allow it"},
		{"changed meanwhile", `{}`, order.ErrStaleState, http.StatusConflict, "invalid_transition", "the order changed meanwhile; read it again"},
		{"long note", `{"note": "x"}`, &order.ValidationError{Field: "note", Reason: "is longer than 500 characters"},
			http.StatusUnprocessableEntity, "validation_failed", "note is longer than 500 characters"},
		{"unknown order", `{}`, order.ErrNotFound, http.StatusNotFound, "not_found", "no such order"},
		{"no body", ``, nil, http.StatusBadRequest, "invalid_request", "the request body is empty"},
		{"unknown field", `{"reason": "x"}`, nil, http.StatusBadRequest, "invalid_request", `unknown field "reason"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &orderAdmin{orders: []order.Order{paidOrder()}, refundErr: tt.err}
			w := send(newOrderAdminAPI(a, ledger{}, &eventLog{}), http.MethodPost,
				"/api/v1/admin/orders/"+pending.ID.String()+"/refund", tt.body)
			assert.Equal(t, tt.status, w.Code)
			code, msg := envelope(t, w.Body)
			assert.Equal(t, tt.code, code)
			assert.Equal(t, tt.msg, msg)
		})
	}
}

func TestAdminRefundAnIDThatIsNotAUUID(t *testing.T) {
	a := &orderAdmin{}
	w := send(newOrderAdminAPI(a, ledger{}, &eventLog{}), http.MethodPost, "/api/v1/admin/orders/x/refund", `{}`)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Empty(t, a.notes)
}
