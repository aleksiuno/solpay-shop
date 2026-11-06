package web

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
)

// OrderAdmin lists, gets and refunds orders for the admin pages. *order.Service is one.
type OrderAdmin interface {
	List(ctx context.Context, f order.ListFilter) ([]order.Order, error)
	Get(ctx context.Context, id uuid.UUID) (order.Order, error)
	Refund(ctx context.Context, id uuid.UUID, note string) (order.Order, error)
}

// maxRefundForm is the largest refund form body that the admin pages read.
const maxRefundForm = 8 << 10

// Ledger lists an order's ledger rows. *db.Payments is one.
type Ledger interface {
	Ledger(ctx context.Context, orderID uuid.UUID) ([]payment.Payment, error)
}

// EventLog lists stored events. *db.Events is one.
type EventLog interface {
	List(ctx context.Context, f audit.ListFilter) ([]audit.Event, error)
}

// adminOrder is an order as the admin pages show it, with the flags that call for a look.
type adminOrder struct {
	order.Order
	Flags []string
}

// newAdminOrder flags o at now: a payment after the deadline, more received than the amount, or
// too little, with the window closed once past the deadline.
func newAdminOrder(o order.Order, now time.Time) adminOrder {
	a := adminOrder{Order: o}
	if o.HasLatePayment {
		a.Flags = append(a.Flags, "late payment")
	}
	if o.ReceivedLamports > o.AmountLamports {
		a.Flags = append(a.Flags, "overpaid")
	}
	if o.Status == order.Underpaid {
		a.Flags = append(a.Flags, "underpaid")
		if !o.OnTime(now) {
			a.Flags = append(a.Flags, "window closed")
		}
	}
	return a
}

// orderList is what the admin's order list shows: the orders, the filter, and what was wrong
// with the filter. The filter offers Statuses in their order.
type orderList struct {
	Orders   []adminOrder
	Status   order.Status // the filter, or "" for every status
	Statuses []order.Status
	Problem  string
}

// adminOrders lists the newest orders, all of them or those in the status that ?status= names.
// Any other value shows the filter with the problem and no orders: 400.
func (s site) adminOrders(w http.ResponseWriter, r *http.Request) {
	status, statuses := order.Status(r.URL.Query().Get("status")), order.Statuses()
	list := orderList{Status: status, Statuses: statuses}
	if status != "" && !slices.Contains(statuses, status) {
		list.Problem = "There is no such status."
		s.render(w, r, http.StatusBadRequest, "admin_orders.html", view{Title: "Orders", Data: list})
		return
	}
	orders, err := s.OrderAdmin.List(r.Context(), order.ListFilter{Status: status})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The clock is read once, so that every row is flagged at the same instant.
	now := s.Now()
	for _, o := range orders {
		list.Orders = append(list.Orders, newAdminOrder(o, now))
	}
	s.render(w, r, http.StatusOK, "admin_orders.html", view{Title: "Orders", Data: list})
}

// orderDetail is what the admin's page for one order shows: the order, its ledger rows and its
// events, whether the refund form is offered, and what was wrong with the last refund asked for.
type orderDetail struct {
	adminOrder
	Payments   []payment.Payment
	Events     []audit.Event
	Refundable bool
	Note       string
	Problem    string
}

// adminOrderPage shows the order whose id is in the path, with its ledger rows and its events. An
// id that is not a UUID names no order.
func (s site) adminOrderPage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, order.ErrNotFound)
		return
	}
	s.showOrder(w, r, http.StatusOK, id, "", "")
}

// refundOrder marks the order whose id is in the path refunded, with the note from the form, and
// sends the browser back to its page with a 303. An order that cannot be refunded, or a note that
// is not acceptable, shows the page again with the problem: 409 or 422.
func (s site) refundOrder(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, order.ErrNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRefundForm)
	if err := r.ParseForm(); err != nil {
		s.problem(w, r, http.StatusBadRequest, "Bad request", "The form could not be read.")
		return
	}
	note := r.PostForm.Get("note")
	o, err := s.OrderAdmin.Refund(r.Context(), id, note)
	var invalid *order.ValidationError
	switch {
	case err == nil:
		http.Redirect(w, r, "/admin/orders/"+o.ID.String(), http.StatusSeeOther)
	case errors.Is(err, order.ErrNoLatePayment):
		s.showOrder(w, r, http.StatusConflict, id, note,
			"An expired order can be refunded only after a payment arrived after its deadline, and none has.")
	case errors.Is(err, order.ErrInvalidTransition):
		s.showOrder(w, r, http.StatusConflict, id, note,
			"This order cannot be refunded: it is refunded already, or it has received nothing.")
	case errors.Is(err, order.ErrStaleState):
		s.showOrder(w, r, http.StatusConflict, id, note, "The order changed while it was being refunded; look at it again.")
	case errors.As(err, &invalid):
		s.showOrder(w, r, http.StatusUnprocessableEntity, id, note, "The note "+invalid.Reason+".")
	default:
		s.fail(w, r, err)
	}
}

// showOrder writes the page of the order with id with status, the refund form holding note, and
// problem.
func (s site) showOrder(w http.ResponseWriter, r *http.Request, status int, id uuid.UUID, note, problem string) {
	ctx := r.Context()
	o, err := s.OrderAdmin.Get(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := orderDetail{adminOrder: newAdminOrder(o, s.Now()), Note: note, Problem: problem}
	// The form is offered where the order may be refunded as it stands; the service checks again,
	// under the store's lock, when the form is sent.
	d.Refundable = o.CheckRefund() == nil
	if d.Payments, err = s.Ledger.Ledger(ctx, id); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Events, err = s.EventLog.List(ctx, audit.ListFilter{OrderID: &id}); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, "admin_order.html", view{Title: "Order", Data: d})
}
