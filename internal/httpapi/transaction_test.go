package httpapi_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/aleksiuno/solpay-shop/internal/httpapi"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
	"github.com/aleksiuno/solpay-shop/internal/solana/solanatest"
)

// builder answers every Build with the same transaction and message, or with err when it is set.
// It notes the orders and payers it was asked for. The transaction's base64 has a "/" and padding,
// as only standard base64 writes it.
type builder struct {
	err    error
	orders []order.Order
	payers []string
}

func (b *builder) Build(_ context.Context, o order.Order, payer string) ([]byte, string, error) {
	b.orders, b.payers = append(b.orders, o), append(b.payers, payer)
	if b.err != nil {
		return nil, "", b.err
	}
	return []byte("unsigned transaction?>"), "solpay-shop (devnet): Go payments checklist (2 licences)", nil
}

const transactionPath = "/api/v1/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e/transaction"

var payer = solanatest.Key("payer")

// newTransactionAPI returns the public routes over s and b, with the clock at now.
func newTransactionAPI(s *orderService, b *builder) (*http.ServeMux, *bytes.Buffer) {
	return newAPI(httpapi.Deps{
		Orders: s, Transactions: b, Merchant: merchant, Now: func() time.Time { return now },
	})
}

func TestOrderTransactionAnswersWithTheTransaction(t *testing.T) {
	b := &builder{}
	mux, _ := newTransactionAPI(&orderService{order: pending}, b)
	w := post(mux, transactionPath, `{"account": "`+payer+`"}`)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.JSONEq(t, `{"transaction": "dW5zaWduZWQgdHJhbnNhY3Rpb24/Pg==",
		"message": "solpay-shop (devnet): Go payments checklist (2 licences)"}`, w.Body.String())
	assert.Equal(t, []order.Order{pending}, b.orders)
	assert.Equal(t, []string{payer}, b.payers)
}

// A request that is not acceptable is refused before a transaction is built, and one whose body
// or account is not acceptable before the order is read. The id is checked first.
func TestOrderTransactionRefusesABadRequest(t *testing.T) {
	const unknown = "/api/v1/orders/6ba7b810-9dad-41d1-80b4-00c04fd430c8/transaction"
	body := `{"account": "` + payer + `"}`
	tests := []struct {
		name, path, body string
		code             int
		ecode, msg       string
		reads            int
	}{
		{"broken JSON", transactionPath, `{"account": "` + payer, http.StatusBadRequest, "invalid_request",
			"the request body is not valid JSON", 0},
		{"an unknown field", transactionPath, `{"account": "` + payer + `", "amount": 1}`, http.StatusBadRequest,
			"invalid_request", `unknown field "amount"`, 0},
		{"a body over 64 KiB", transactionPath, `{"account": "` + strings.Repeat("1", 64<<10) + `"}`,
			http.StatusBadRequest, "invalid_request", "the request body is larger than 65536 bytes", 0},
		{"an account of 31 bytes", transactionPath, `{"account": "` + strings.Repeat("1", 31) + `"}`,
			http.StatusUnprocessableEntity, "validation_failed", "account is not a base58 address of 32 bytes", 0},
		{"an account of 65,000 characters", transactionPath, `{"account": "` + strings.Repeat("z", 65_000) + `"}`,
			http.StatusUnprocessableEntity, "validation_failed", "account is not a base58 address of 32 bytes", 0},
		{"no account", transactionPath, `{}`, http.StatusUnprocessableEntity, "validation_failed",
			"account is required", 0},
		{"the merchant's address", transactionPath, `{"account": "` + merchant + `"}`, http.StatusUnprocessableEntity,
			"validation_failed", "account is the shop's own address, which cannot pay itself", 0},
		{"an unknown order", unknown, body, http.StatusNotFound, "not_found", "no such order", 1},
		{"an id that is not a UUID", "/api/v1/orders/not-a-uuid/transaction", body, http.StatusNotFound,
			"not_found", "no such order", 0},
		{"an id that is not a UUID and broken JSON", "/api/v1/orders/not-a-uuid/transaction", "{",
			http.StatusNotFound, "not_found", "no such order", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, b := &orderService{order: pending}, &builder{}
			mux, _ := newTransactionAPI(s, b)
			w := post(mux, tt.path, tt.body)

			assert.Equal(t, tt.code, w.Code)
			assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			code, msg := envelope(t, w.Body)
			assert.Equal(t, tt.ecode, code)
			assert.Equal(t, tt.msg, msg)
			assert.Equal(t, tt.reads, s.calls, "reads of the order")
			assert.Empty(t, b.orders, "no transaction is built")
		})
	}
}

// A transaction is built only while the order has a payment block, by the same clock.
func TestOrderTransactionOnlyForAnOrderThatCanBePaidNow(t *testing.T) {
	paidAt := now.Add(-time.Minute)
	tests := []struct {
		name    string
		change  func(o *order.Order)
		payable bool
	}{
		{"pending at its deadline", func(o *order.Order) { o.ExpiresAt = now }, true},
		{"pending past its deadline", func(o *order.Order) { o.ExpiresAt = now.Add(-time.Nanosecond) }, false},
		{"underpaid", func(o *order.Order) { o.Status, o.ReceivedLamports = order.Underpaid, 4_000_000 }, true},
		{"paid", func(o *order.Order) { o.Status, o.ReceivedLamports, o.PaidAt = order.Paid, 10_000_000, &paidAt }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := pending
			tt.change(&o)
			b := &builder{}
			mux, _ := newTransactionAPI(&orderService{order: o}, b)
			w := post(mux, transactionPath, `{"account": "`+payer+`"}`)

			if tt.payable {
				assert.Equal(t, http.StatusOK, w.Code)
				assert.Equal(t, []order.Order{o}, b.orders)
				return
			}
			assert.Equal(t, http.StatusConflict, w.Code)
			code, msg := envelope(t, w.Body)
			assert.Equal(t, "not_payable", code)
			assert.Equal(t, "the order can be paid only while it is pending or underpaid, before its deadline", msg)
			assert.Empty(t, b.orders, "no blockhash is fetched")
		})
	}
}

// When the RPC node gives no blockhash, the answer is a 503 whose Retry-After says, in whole
// seconds rounded up, when the shop asks the node again. The node's error is neither sent nor
// logged again, since the cache logs it once for all the requests that waited.
func TestOrderTransactionWhenTheNodeFails(t *testing.T) {
	for retry, header := range map[time.Duration]string{0: "1", 1500 * time.Millisecond: "2", 15 * time.Second: "15"} {
		err := &payment.NoBlockhashError{Err: errors.New(`Post "https://devnet.example/": refused`), RetryAfter: retry}
		mux, buf := newTransactionAPI(&orderService{order: pending}, &builder{err: err})
		w := post(mux, transactionPath, `{"account": "`+payer+`"}`)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		assert.Equal(t, header, w.Header().Get("Retry-After"), retry)
		code, msg := envelope(t, w.Body)
		assert.Equal(t, "rpc_unavailable", code)
		assert.Equal(t, "the Solana RPC node is unavailable; try again shortly", msg)
		assert.Empty(t, buf.String())
	}
}
