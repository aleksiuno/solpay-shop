package httpapi

import (
	"context"
	"net/http"
	"time"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/order"
)

// OrderAdmin refunds orders for the admin API. *order.Service is one.
type OrderAdmin interface {
	Refund(ctx context.Context, id uuid.UUID, note string) (order.Order, error)
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
