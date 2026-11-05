package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/httpapi"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
	"github.com/aleksiuno/solpay-shop/internal/product"
	"github.com/aleksiuno/solpay-shop/internal/solana/solanatest"
)

// orderService holds one order, which Create returns and Get finds by its id. A set err is
// returned by every call instead. It notes what Create was asked for.
type orderService struct {
	order order.Order
	err   error
	slug  string
	qty   int
	calls int
}

func (s *orderService) Create(_ context.Context, slug string, qty int) (order.Order, error) {
	s.calls++
	s.slug, s.qty = slug, qty
	if s.err != nil {
		return order.Order{}, s.err
	}
	return s.order, nil
}

func (s *orderService) Get(_ context.Context, id uuid.UUID) (order.Order, error) {
	s.calls++
	if s.err != nil {
		return order.Order{}, s.err
	}
	if id != s.order.ID {
		return order.Order{}, order.ErrNotFound
	}
	return s.order, nil
}

// links gives the link it holds for an order, when ok is set, and notes the orders it is asked
// about.
type links struct {
	link  string
	ok    bool
	asked []order.Order
}

func (l *links) Link(o order.Order) (string, bool) {
	l.asked = append(l.asked, o)
	return l.link, l.ok
}

var (
	merchant  = solanatest.Key("merchant")
	reference = solanatest.Key("reference")
	now       = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	// pending is a new order for two licences, made at now, with times in another zone.
	pending = order.Order{
		ID:             uuid.MustParse("3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e"),
		ProductSlug:    "go-payments-checklist",
		ProductTitle:   "Go payments checklist",
		Quantity:       2,
		AmountLamports: 10_000_000,
		Reference:      reference,
		Status:         order.Pending,
		ExpiresAt:      now.Add(15 * time.Minute).In(time.FixedZone("EET", 2*60*60)),
		CreatedAt:      now.In(time.FixedZone("EET", 2*60*60)),
	}
)

// pendingJSON is pending as the API shows it at now, with its payment request.
var pendingJSON = `{
	"id": "3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e", "status": "pending",
	"product": {"slug": "go-payments-checklist", "title": "Go payments checklist"},
	"quantity": 2, "amount_lamports": 10000000, "received_lamports": 0, "remaining_lamports": 10000000,
	"payment": {"recipient": "` + merchant + `", "reference": "` + reference + `",
		"url": "solana:` + merchant + `?amount=0.01&reference=` + reference +
	`&label=solpay-shop%20%28devnet%29&message=Go%20payments%20checklist%20%282%20licences%29"},
	"expires_at": "2026-11-03T12:15:00Z", "paid_at": null, "download_url": null,
	"created_at": "2026-11-03T12:00:00Z"}`

// newOrdersAPI returns the public routes over s, with the clock at now and no download links.
func newOrdersAPI(s *orderService) (*http.ServeMux, *bytes.Buffer) {
	return newOrdersAPIWithLinks(s, &links{})
}

// newOrdersAPIWithLinks is newOrdersAPI with download links from l.
func newOrdersAPIWithLinks(s *orderService, l *links) (*http.ServeMux, *bytes.Buffer) {
	return newAPI(httpapi.Deps{
		Products: &catalogue{}, Orders: s, Downloads: l, Merchant: merchant, Now: func() time.Time { return now },
	})
}

func post(h http.Handler, path, body string) *httptest.ResponseRecorder {
	return serve(h, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
}

func TestCreateOrder(t *testing.T) {
	s := &orderService{order: pending}
	mux, _ := newOrdersAPI(s)
	w := post(mux, "/api/v1/orders", `{"product_slug": "go-payments-checklist", "quantity": 2}`)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "/api/v1/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e", w.Header().Get("Location"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.JSONEq(t, pendingJSON, w.Body.String())
	assert.Equal(t, "go-payments-checklist", s.slug)
	assert.Equal(t, 2, s.qty)
}

func TestCreateOrderRefusesABadBody(t *testing.T) {
	tests := []struct {
		name, body string
		code       int
		want       string
	}{
		{"an empty body", ``, http.StatusBadRequest, "the request body is empty"},
		{"broken JSON", `{"product_slug": "go-payments-checklist"`, http.StatusBadRequest, "the request body is not valid JSON"},
		{"an array", `["go-payments-checklist", 2]`, http.StatusBadRequest, "the request body must be a JSON object"},
		{"null", ` null `, http.StatusBadRequest, "the request body must be a JSON object"},
		{"a number", `2`, http.StatusBadRequest, "the request body must be a JSON object"},
		{"a quantity as a string", `{"product_slug": "go-payments-checklist", "quantity": "2"}`, http.StatusBadRequest,
			"quantity has the wrong type"},
		{"a fractional quantity", `{"product_slug": "go-payments-checklist", "quantity": 1.5}`, http.StatusBadRequest,
			"quantity has the wrong type"},
		{"an unknown field", `{"product_slug": "go-payments-checklist", "quantity": 2, "colour": "red"}`,
			http.StatusBadRequest, `unknown field "colour"`},
		{"two values", `{"product_slug": "go-payments-checklist", "quantity": 2} {}`, http.StatusBadRequest,
			"the request body must hold one JSON value and nothing after it"},
		{"something after the value", `{"product_slug": "go-payments-checklist", "quantity": 2} garbage`,
			http.StatusBadRequest, "the request body must hold one JSON value and nothing after it"},
		{"a body over 64 KiB", `{"product_slug": "` + strings.Repeat("a", 64<<10) + `", "quantity": 2}`,
			http.StatusBadRequest, "the request body is larger than 65536 bytes"},
		{"a value padded past 64 KiB", `{"product_slug": "go-payments-checklist", "quantity": 2}` +
			strings.Repeat(" ", 64<<10), http.StatusBadRequest, "the request body is larger than 65536 bytes"},
		{"no product", `{"quantity": 2}`, http.StatusUnprocessableEntity, "product_slug is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &orderService{order: pending}
			mux, _ := newOrdersAPI(s)
			w := post(mux, "/api/v1/orders", tt.body)

			assert.Equal(t, tt.code, w.Code)
			code, msg := envelope(t, w.Body)
			if tt.code == http.StatusBadRequest {
				assert.Equal(t, "invalid_request", code)
			} else {
				assert.Equal(t, "validation_failed", code)
			}
			assert.Equal(t, tt.want, msg)
			assert.Zero(t, s.calls, "the service is not asked")
		})
	}
}

func TestCreateOrderAnswersTheServicesErrors(t *testing.T) {
	tests := []struct {
		err        error
		code       int
		ecode, msg string
	}{
		{&order.ValidationError{Field: "quantity", Reason: "must be from 1 to 10"},
			http.StatusUnprocessableEntity, "validation_failed", "quantity must be from 1 to 10"},
		{product.ErrNotFound, http.StatusNotFound, "not_found", "no such product"},
		{order.ErrProductInactive, http.StatusConflict, "product_inactive", "the product is not for sale"},
		{errors.New("connection reset"), http.StatusInternalServerError, "internal", "internal error"},
	}
	for _, tt := range tests {
		t.Run(tt.ecode, func(t *testing.T) {
			mux, _ := newOrdersAPI(&orderService{err: tt.err})
			w := post(mux, "/api/v1/orders", `{"product_slug": "go-payments-checklist", "quantity": 11}`)

			assert.Equal(t, tt.code, w.Code)
			assert.Empty(t, w.Header().Values("Location"))
			code, msg := envelope(t, w.Body)
			assert.Equal(t, tt.ecode, code)
			assert.Equal(t, tt.msg, msg)
		})
	}
}

func TestGetOrder(t *testing.T) {
	s := &orderService{order: pending}
	mux, _ := newOrdersAPI(s)
	w := get(mux, "/api/v1/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.JSONEq(t, pendingJSON, w.Body.String())
	assert.Contains(t, w.Body.String(), "&reference="+reference, `"&" as it is, so the URL can be copied`)
}

func TestGetOrderThatDoesNotExist(t *testing.T) {
	tests := []struct {
		id    string
		calls int
	}{
		{"6ba7b810-9dad-41d1-80b4-00c04fd430c8", 1},
		{"not-a-uuid", 0}, // names no order, so the service is not asked
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			s := &orderService{order: pending}
			mux, _ := newOrdersAPI(s)
			w := get(mux, "/api/v1/orders/"+tt.id)

			assert.Equal(t, http.StatusNotFound, w.Code)
			code, msg := envelope(t, w.Body)
			assert.Equal(t, "not_found", code)
			assert.Equal(t, "no such order", msg)
			assert.Equal(t, tt.calls, s.calls)
		})
	}
}

func TestGetOrderWhenTheStoreFails(t *testing.T) {
	mux, buf := newOrdersAPI(&orderService{err: errors.New("connection reset")})
	w := serve(httpapi.RequestID(mux), httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+pending.ID.String(), nil))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	code, _ := envelope(t, w.Body)
	assert.Equal(t, "internal", code)
	lines := logLines(t, buf)
	require.Len(t, lines, 1)
	assert.Equal(t, "ERROR", lines[0]["level"])
	assert.Equal(t, "connection reset", lines[0]["error"])
	assert.Equal(t, w.Header().Get("X-Request-ID"), lines[0]["request_id"])
}

func TestGetUnderpaidOrder(t *testing.T) {
	o := pending
	o.Status, o.ReceivedLamports = order.Underpaid, 4_000_000
	mux, _ := newOrdersAPI(&orderService{order: o})
	w := get(mux, "/api/v1/orders/"+o.ID.String())

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{
		"id": "3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e", "status": "underpaid",
		"product": {"slug": "go-payments-checklist", "title": "Go payments checklist"},
		"quantity": 2, "amount_lamports": 10000000, "received_lamports": 4000000, "remaining_lamports": 6000000,
		"payment": {"recipient": "`+merchant+`", "reference": "`+reference+`",
			"url": "solana:`+merchant+`?amount=0.006&reference=`+reference+
		`&label=solpay-shop%20%28devnet%29&message=Go%20payments%20checklist%20%282%20licences%29"},
		"expires_at": "2026-11-03T12:15:00Z", "paid_at": null, "download_url": null,
		"created_at": "2026-11-03T12:00:00Z"}`, w.Body.String())
}

// The payment block is there only while the order can be paid: pending or underpaid, and not past
// its deadline.
func TestOrderPaymentBlock(t *testing.T) {
	paidAt := now.Add(-time.Minute)
	tests := []struct {
		name    string
		change  func(o *order.Order)
		payable bool
	}{
		{"pending", func(*order.Order) {}, true},
		{"pending at its deadline", func(o *order.Order) { o.ExpiresAt = now }, true},
		{"pending past its deadline", func(o *order.Order) { o.ExpiresAt = now.Add(-time.Nanosecond) }, false},
		{"underpaid", func(o *order.Order) { o.Status, o.ReceivedLamports = order.Underpaid, 4_000_000 }, true},
		{"underpaid past its deadline", func(o *order.Order) {
			o.Status, o.ReceivedLamports, o.ExpiresAt = order.Underpaid, 4_000_000, now.Add(-time.Second)
		}, false},
		{"paid", func(o *order.Order) { o.Status, o.ReceivedLamports, o.PaidAt = order.Paid, 10_000_000, &paidAt }, false},
		{"fulfilled", func(o *order.Order) { o.Status, o.PaidAt = order.Fulfilled, &paidAt }, false},
		{"expired", func(o *order.Order) { o.Status = order.Expired }, false},
		{"refunded", func(o *order.Order) { o.Status = order.Refunded }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := pending
			tt.change(&o)
			mux, _ := newOrdersAPI(&orderService{order: o})
			var got struct {
				Payment *struct{ Recipient, Reference, URL string }
			}
			w := get(mux, "/api/v1/orders/"+o.ID.String())
			require.Equal(t, http.StatusOK, w.Code)
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))

			if !tt.payable {
				assert.Contains(t, w.Body.String(), `"payment":null`, "there, and null")
				return
			}
			require.NotNil(t, got.Payment)
			assert.Equal(t, merchant, got.Payment.Recipient)
			assert.Equal(t, reference, got.Payment.Reference)
			assert.Equal(t, payment.Request(o, merchant).URL(), got.Payment.URL, "for what remains")
		})
	}
}

func TestOrderShowsWhenItWasPaid(t *testing.T) {
	o := pending
	paidAt := now.Add(90 * time.Second).In(time.FixedZone("EET", 2*60*60))
	o.Status, o.ReceivedLamports, o.PaidAt = order.Paid, 10_000_000, &paidAt
	mux, _ := newOrdersAPI(&orderService{order: o})
	w := get(mux, "/api/v1/orders/"+o.ID.String())

	assert.Contains(t, w.Body.String(), `"paid_at":"2026-11-03T12:01:30Z"`)
	assert.Contains(t, w.Body.String(), `"remaining_lamports":0`)
}

// The download link is the one the download service gives for the order, and null when it gives
// none.
func TestOrderDownloadLink(t *testing.T) {
	o := pending
	paidAt := now.Add(-time.Minute)
	o.Status, o.ReceivedLamports, o.PaidAt = order.Paid, 10_000_000, &paidAt
	const link = "/d/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e?exp=1793535300&sig=c2lnbmF0dXJl"
	for _, ok := range []bool{true, false} {
		l := &links{link: link, ok: ok}
		mux, _ := newOrdersAPIWithLinks(&orderService{order: o}, l)
		w := get(mux, "/api/v1/orders/"+o.ID.String())
		require.Equal(t, http.StatusOK, w.Code)

		if ok {
			assert.Contains(t, w.Body.String(), `"download_url":"`+link+`"`)
		} else {
			assert.Contains(t, w.Body.String(), `"download_url":null`, "there, and null")
		}
		assert.Equal(t, []order.Order{o}, l.asked)
	}
}
