package web

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

// OrderAdmin lists and gets orders for the admin pages. *order.Service is one.
type OrderAdmin interface {
	List(ctx context.Context, f order.ListFilter) ([]order.Order, error)
	Get(ctx context.Context, id uuid.UUID) (order.Order, error)
}

// Ledger lists an order's ledger rows. *db.Payments is one.
type Ledger interface {
	Ledger(ctx context.Context, orderID uuid.UUID) ([]payment.Payment, error)
}

// EventLog lists stored events. *db.Events is one.
type EventLog interface {
	List(ctx context.Context, f audit.ListFilter) ([]audit.Event, error)
}

// statuses are the order statuses, in the order the admin's filter offers them.
var statuses = []order.Status{
	order.Pending, order.Underpaid, order.Paid, order.Fulfilled, order.Expired, order.Refunded,
}

// adminOrder is an order as the admin pages show it, with the flags that call for a look.
type adminOrder struct {
	order.Order
	Flags []string
}

// newAdminOrder flags o at now: a payment after the deadline, more received than the amount, or
// too little, with the window closed once past the deadline.
func newAdminOrder(o order.Order, now func() time.Time) adminOrder {
	a := adminOrder{Order: o}
	if o.HasLatePayment {
		a.Flags = append(a.Flags, "late payment")
	}
	if o.ReceivedLamports > o.AmountLamports {
		a.Flags = append(a.Flags, "overpaid")
	}
	if o.Status == order.Underpaid {
		a.Flags = append(a.Flags, "underpaid")
		if !o.OnTime(now()) {
			a.Flags = append(a.Flags, "window closed")
		}
	}
	return a
}

// orderList is what the admin's order list shows.
type orderList struct {
	Orders   []adminOrder
	Status   order.Status // the filter, or "" for every status
	Statuses []order.Status
}

// adminOrders lists the newest orders, all of them or those in the status that ?status= names.
func (s site) adminOrders(w http.ResponseWriter, r *http.Request) {
	status := order.Status(r.URL.Query().Get("status"))
	if status != "" && !slices.Contains(statuses, status) {
		s.problem(w, r, http.StatusBadRequest, "Bad request", "There is no such status.")
		return
	}
	orders, err := s.OrderAdmin.List(r.Context(), order.ListFilter{Status: status})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	list := orderList{Status: status, Statuses: statuses}
	for _, o := range orders {
		list.Orders = append(list.Orders, newAdminOrder(o, s.Now))
	}
	s.render(w, r, http.StatusOK, "admin_orders.html", view{Title: "Orders", Data: list})
}

// orderDetail is what the admin's page for one order shows: the order, its ledger rows and its
// events.
type orderDetail struct {
	adminOrder
	Payments []payment.Payment
	Events   []audit.Event
}

// adminOrderPage shows the order whose id is in the path, with its ledger rows and its events. An
// id that is not a UUID names no order.
func (s site) adminOrderPage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, order.ErrNotFound)
		return
	}
	ctx := r.Context()
	o, err := s.OrderAdmin.Get(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := orderDetail{adminOrder: newAdminOrder(o, s.Now)}
	if d.Payments, err = s.Ledger.Ledger(ctx, id); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Events, err = s.EventLog.List(ctx, audit.ListFilter{OrderID: &id}); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "admin_order.html", view{Title: "Order", Data: d})
}
