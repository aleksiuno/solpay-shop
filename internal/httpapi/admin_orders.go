package httpapi

import (
	"context"
	"net/http"
	"slices"
	"time"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
)

// OrderAdmin lists, gets and refunds orders for the admin API. *order.Service is one.
type OrderAdmin interface {
	List(ctx context.Context, f order.ListFilter) ([]order.Order, error)
	Get(ctx context.Context, id uuid.UUID) (order.Order, error)
	Refund(ctx context.Context, id uuid.UUID, note string) (order.Order, error)
}

// Ledger lists an order's ledger rows. *db.Payments is one.
type Ledger interface {
	Ledger(ctx context.Context, orderID uuid.UUID) ([]payment.Payment, error)
}

// EventLog lists stored events. *db.Events is one.
type EventLog interface {
	List(ctx context.Context, f audit.ListFilter) ([]audit.Event, error)
}

// orderStatuses are the statuses that ?status= may name.
var orderStatuses = []order.Status{
	order.Pending, order.Underpaid, order.Paid, order.Fulfilled, order.Expired, order.Refunded,
}

// adminOrderJSON is an order as the admin API shows it: what the store holds, without the payment
// request and the download link that the public API adds.
type adminOrderJSON struct {
	ID      uuid.UUID    `json:"id"`
	Status  order.Status `json:"status"`
	Product struct {
		Slug  string `json:"slug"`
		Title string `json:"title"`
	} `json:"product"`
	Quantity         int        `json:"quantity"`
	AmountLamports   int64      `json:"amount_lamports"`
	ReceivedLamports int64      `json:"received_lamports"`
	Reference        string     `json:"reference"`
	HasLatePayment   bool       `json:"has_late_payment"`
	ExpiresAt        time.Time  `json:"expires_at"`
	CreatedAt        time.Time  `json:"created_at"`
	PaidAt           *time.Time `json:"paid_at"`
	FulfilledAt      *time.Time `json:"fulfilled_at"`
	RefundedAt       *time.Time `json:"refunded_at"`
}

func newAdminOrderJSON(o order.Order) adminOrderJSON {
	j := adminOrderJSON{
		ID: o.ID, Status: o.Status, Quantity: o.Quantity, AmountLamports: o.AmountLamports,
		ReceivedLamports: o.ReceivedLamports, Reference: o.Reference, HasLatePayment: o.HasLatePayment,
		ExpiresAt: o.ExpiresAt.UTC(), CreatedAt: o.CreatedAt.UTC(),
		PaidAt: utc(o.PaidAt), FulfilledAt: utc(o.FulfilledAt), RefundedAt: utc(o.RefundedAt),
	}
	j.Product.Slug, j.Product.Title = o.ProductSlug, o.ProductTitle
	return j
}

// utc returns t in UTC, or nil.
func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// ledgerRowJSON is a ledger row: what one transaction did for the order.
type ledgerRowJSON struct {
	Signature string               `json:"signature"`
	Outcome   payment.Outcome      `json:"outcome"`
	Reason    payment.RejectReason `json:"reason,omitempty"`
	Lamports  int64                `json:"lamports"`
	Slot      int64                `json:"slot"`
	BlockTime *time.Time           `json:"block_time"`
}

// eventJSON is a stored event.
type eventJSON struct {
	ID        int64          `json:"id"`
	OrderID   *uuid.UUID     `json:"order_id"`
	Level     audit.Level    `json:"level"`
	Kind      audit.Kind     `json:"kind"`
	Message   string         `json:"message"`
	Signature string         `json:"signature"`
	Data      map[string]any `json:"data"`
	CreatedAt time.Time      `json:"created_at"`
}

// listAdminOrders answers with the newest orders, all of them or those in the status that
// ?status= names: {"orders": [...]}.
func (a api) listAdminOrders(w http.ResponseWriter, r *http.Request) {
	status := order.Status(r.URL.Query().Get("status"))
	if status != "" && !slices.Contains(orderStatuses, status) {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "status must be one of pending, underpaid, paid, fulfilled, expired, refunded")
		return
	}
	orders, err := a.OrderAdmin.List(r.Context(), order.ListFilter{Status: status})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := struct {
		Orders []adminOrderJSON `json:"orders"`
	}{make([]adminOrderJSON, 0, len(orders))}
	for _, o := range orders {
		out.Orders = append(out.Orders, newAdminOrderJSON(o))
	}
	writeJSON(w, http.StatusOK, out)
}

// getAdminOrder answers with the order whose id is in the path, with its ledger rows and its
// events, the newest event first.
func (a api) getAdminOrder(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, order.ErrNotFound)
		return
	}
	ctx := r.Context()
	o, err := a.OrderAdmin.Get(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	payments, err := a.Ledger.Ledger(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	events, err := a.EventLog.List(ctx, audit.ListFilter{OrderID: &id})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := struct {
		adminOrderJSON
		Payments []ledgerRowJSON `json:"payments"`
		Events   []eventJSON     `json:"events"`
	}{newAdminOrderJSON(o), make([]ledgerRowJSON, 0, len(payments)), make([]eventJSON, 0, len(events))}
	for _, p := range payments {
		out.Payments = append(out.Payments, ledgerRowJSON{
			Signature: p.Signature, Outcome: p.Outcome, Reason: p.Reason, Lamports: p.Lamports, Slot: p.Slot,
			BlockTime: utc(p.BlockTime),
		})
	}
	for _, ev := range events {
		out.Events = append(out.Events, eventJSON{
			ID: ev.ID, OrderID: ev.OrderID, Level: ev.Kind.Level(), Kind: ev.Kind, Message: ev.Message,
			Signature: ev.Signature, Data: ev.Data, CreatedAt: ev.CreatedAt.UTC(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// refundAdminOrder marks the order whose id is in the path refunded, with the note in the body,
// {"note": "..."}, and answers with the order.
func (a api) refundAdminOrder(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, order.ErrNotFound)
		return
	}
	var b struct {
		Note string `json:"note"`
	}
	if msg := decode(w, r, &b); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	o, err := a.OrderAdmin.Refund(r.Context(), id, b.Note)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newAdminOrderJSON(o))
}
