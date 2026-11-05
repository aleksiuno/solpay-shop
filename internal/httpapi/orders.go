package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
)

// maxBody is the largest request body the API reads.
const maxBody = 64 << 10

// orderJSON is an order as the API shows it.
type orderJSON struct {
	ID      uuid.UUID    `json:"id"`
	Status  order.Status `json:"status"`
	Product struct {
		Slug  string `json:"slug"`
		Title string `json:"title"`
	} `json:"product"`
	Quantity          int          `json:"quantity"`
	AmountLamports    int64        `json:"amount_lamports"`
	ReceivedLamports  int64        `json:"received_lamports"`
	RemainingLamports int64        `json:"remaining_lamports"`
	Payment           *paymentJSON `json:"payment"` // null unless the order can be paid now
	ExpiresAt         time.Time    `json:"expires_at"`
	PaidAt            *time.Time   `json:"paid_at"`
	DownloadURL       *string      `json:"download_url"` // null unless the order can be downloaded
	CreatedAt         time.Time    `json:"created_at"`
}

// paymentJSON says how to pay what remains of an order: the Solana Pay URL, and the recipient and
// reference it names, for a client that builds the transfer itself.
type paymentJSON struct {
	Recipient string `json:"recipient"`
	Reference string `json:"reference"`
	URL       string `json:"url"`
}

func (a api) newOrderJSON(o order.Order) orderJSON {
	j := orderJSON{
		ID:                o.ID,
		Status:            o.Status,
		Quantity:          o.Quantity,
		AmountLamports:    o.AmountLamports,
		ReceivedLamports:  o.ReceivedLamports,
		RemainingLamports: o.RemainingLamports(),
		ExpiresAt:         o.ExpiresAt.UTC(),
		CreatedAt:         o.CreatedAt.UTC(),
	}
	j.Product.Slug, j.Product.Title = o.ProductSlug, o.ProductTitle
	if o.PaidAt != nil {
		t := o.PaidAt.UTC()
		j.PaidAt = &t
	}
	// An order takes payments while it is pending or underpaid, until its deadline.
	if (o.Status == order.Pending || o.Status == order.Underpaid) && o.OnTime(a.Now()) {
		j.Payment = &paymentJSON{
			Recipient: a.Merchant,
			Reference: o.Reference,
			URL:       payment.Request(o, a.Merchant).URL(),
		}
	}
	// A paid order gets a new link on every read, since each one works for a short time only.
	if link, ok := a.Downloads.Link(o); ok {
		j.DownloadURL = &link
	}
	return j
}

// writeOrder writes o with status. The order id is the customer's only key to the order, so no
// cache may keep the response.
func (a api) writeOrder(w http.ResponseWriter, status int, o order.Order) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, a.newOrderJSON(o))
}

// createOrder places an order from a body of {"product_slug": "...", "quantity": n} and answers
// 201 with the order and its Location.
func (a api) createOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProductSlug string `json:"product_slug"`
		Quantity    int    `json:"quantity"`
	}
	if msg := decode(w, r, &req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	if req.ProductSlug == "" {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "product_slug is required")
		return
	}
	o, err := a.Orders.Create(r.Context(), req.ProductSlug, req.Quantity)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/orders/"+o.ID.String())
	a.writeOrder(w, http.StatusCreated, o)
}

// getOrder answers with the order whose id is in the path. An id that is not a UUID names no
// order, so it gets the same not_found as an unknown one.
func (a api) getOrder(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, order.ErrNotFound)
		return
	}
	o, err := a.Orders.Get(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeOrder(w, http.StatusOK, o)
}

// decode reads r's body into v. The body must be one JSON object of at most maxBody bytes, with no
// fields that v lacks. decode returns "" on success, or a message for the client that says what is
// wrong.
func decode(w http.ResponseWriter, r *http.Request, v any) string {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	var raw json.RawMessage
	if err := d.Decode(&raw); err != nil {
		return bodyProblem(err)
	}
	switch err := d.Decode(&json.RawMessage{}); {
	case errors.Is(err, io.EOF):
	case errors.As(err, new(*http.MaxBytesError)):
		return bodyProblem(err)
	default:
		return "the request body must hold one JSON value and nothing after it"
	}
	// A null would decode into v without an error and leave it empty.
	if raw[0] != '{' {
		return "the request body must be a JSON object"
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return bodyProblem(err)
	}
	return ""
}

// bodyProblem says what err, from decoding a request body, means for the client.
func bodyProblem(err error) string {
	var wrongType *json.UnmarshalTypeError
	switch {
	case errors.Is(err, io.EOF):
		return "the request body is empty"
	case errors.As(err, new(*http.MaxBytesError)):
		return fmt.Sprintf("the request body is larger than %d bytes", maxBody)
	case errors.As(err, &wrongType):
		return wrongType.Field + " has the wrong type"
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return strings.TrimPrefix(err.Error(), "json: ")
	}
	return "the request body is not valid JSON"
}
