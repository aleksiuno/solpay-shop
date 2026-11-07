package httpapi_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/httpapi"
)

// pinger answers Ping with err, and notes how long the context it got had left before its deadline.
type pinger struct {
	err      error
	deadline time.Duration
}

func (p *pinger) Ping(ctx context.Context) error {
	if d, ok := ctx.Deadline(); ok {
		p.deadline = time.Until(d)
	}
	return p.err
}

// newAPI returns a mux with the public routes over d, logging to the returned buffer.
func newAPI(d httpapi.Deps) (*http.ServeMux, *bytes.Buffer) {
	log, buf := newLogger()
	d.Log = log
	mux := http.NewServeMux()
	httpapi.Register(mux, d)
	return mux, buf
}

func TestHealthWhenTheDatabaseAnswers(t *testing.T) {
	db := &pinger{}
	mux, buf := newAPI(httpapi.Deps{DB: db})
	w := serve(mux, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ok\n", w.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Positive(t, db.deadline, "the ping is bounded")
	assert.LessOrEqual(t, db.deadline, 2*time.Second)
	assert.Empty(t, buf.String())
}

func TestHealthWhenTheDatabaseDoesNotAnswer(t *testing.T) {
	mux, buf := newAPI(httpapi.Deps{DB: &pinger{err: errors.New("connection refused")}})
	w := serve(httpapi.RequestID(mux), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "database unavailable\n", w.Body.String(), "the error is not sent")
	lines := logLines(t, buf)
	require.Len(t, lines, 1)
	assert.Equal(t, "WARN", lines[0]["level"])
	assert.Equal(t, "connection refused", lines[0]["error"])
	assert.Equal(t, w.Header().Get("X-Request-ID"), lines[0]["request_id"], "logged with the request's context")
}

func TestAdminAPIAnswersAnUnknownPathWithNotFound(t *testing.T) {
	mux := http.NewServeMux()
	httpapi.RegisterAdmin(mux, httpapi.Deps{})
	for _, path := range []string{"/api/v1/admin/", "/api/v1/admin/no-such-endpoint", "/api/v1/admin/a/b"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			w := serve(mux, httptest.NewRequest(method, path, nil))
			assert.Equal(t, http.StatusNotFound, w.Code, path)
			code, _ := envelope(t, w.Body)
			assert.Equal(t, "not_found", code, path)
		}
	}
}

func TestUnknownAPIPaths(t *testing.T) {
	mux, _ := newAPI(httpapi.Deps{Products: &catalogue{}})
	tests := []struct {
		method, path string
		code         int
		allow        string
	}{
		{http.MethodGet, "/api/v1/", http.StatusNotFound, ""},
		{http.MethodGet, "/api/v1/no-such-endpoint", http.StatusNotFound, ""},
		{http.MethodPost, "/api/v1/no-such-endpoint", http.StatusNotFound, ""},
		{http.MethodGet, "/api/v1/products/a/b", http.StatusNotFound, ""},
		{http.MethodPost, "/api/v1/products", http.StatusMethodNotAllowed, "GET, HEAD"},
		{http.MethodDelete, "/api/v1/products/go-payments-checklist", http.StatusMethodNotAllowed, "GET, HEAD"},
		{http.MethodGet, "/api/v1/orders", http.StatusMethodNotAllowed, "POST"},
		{http.MethodPost, "/api/v1/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e", http.StatusMethodNotAllowed, "GET, HEAD"},
		{http.MethodGet, "/api/v1/orders/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e/transaction", http.StatusMethodNotAllowed, "POST"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			w := serve(mux, httptest.NewRequest(tt.method, tt.path, nil))
			assert.Equal(t, tt.code, w.Code)
			assert.Equal(t, tt.allow, w.Header().Get("Allow"))
			code, _ := envelope(t, w.Body)
			if tt.code == http.StatusNotFound {
				assert.Equal(t, "not_found", code)
			} else {
				assert.Equal(t, "method_not_allowed", code)
			}
		})
	}
}

func TestAdminAPIAnswersAWrongMethodWithMethodNotAllowed(t *testing.T) {
	mux := http.NewServeMux()
	httpapi.RegisterAdmin(mux, httpapi.Deps{})
	w := serve(mux, httptest.NewRequest(http.MethodGet, "/api/v1/admin/orders/1/refund", nil))

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Equal(t, "POST", w.Header().Get("Allow"))
	code, _ := envelope(t, w.Body)
	assert.Equal(t, "method_not_allowed", code)
}

func TestForbidden(t *testing.T) {
	w := httptest.NewRecorder()
	httpapi.Forbidden(w, httptest.NewRequest(http.MethodPost, "/", nil))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	code, msg := envelope(t, w.Body)
	assert.Equal(t, "forbidden", code)
	assert.NotEmpty(t, msg)
}
