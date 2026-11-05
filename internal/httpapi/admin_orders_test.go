package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"

	"github.com/aleksiuno/solpay-shop/internal/httpapi"
	"github.com/aleksiuno/solpay-shop/internal/order"
)

// orderAdmin holds orders. A set refundErr is returned by Refund instead. It notes the notes of
// the refunds asked for.
type orderAdmin struct {
	orders    []order.Order
	refundErr error
	notes     []string
}

func (a *orderAdmin) Get(_ context.Context, id uuid.UUID) (order.Order, error) {
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

// newOrderAdminAPI returns the admin routes over a.
func newOrderAdminAPI(a *orderAdmin) *http.ServeMux {
	log, _ := newLogger()
	mux := http.NewServeMux()
	httpapi.RegisterAdmin(mux, httpapi.Deps{OrderAdmin: a, Log: log})
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

func TestAdminRefundsAnOrder(t *testing.T) {
	a := &orderAdmin{orders: []order.Order{paidOrder()}}
	w := send(newOrderAdminAPI(a), http.MethodPost,
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
			w := send(newOrderAdminAPI(a), http.MethodPost,
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
	w := send(newOrderAdminAPI(a), http.MethodPost, "/api/v1/admin/orders/x/refund", `{}`)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Empty(t, a.notes)
}
