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

func newMux(db httpapi.Pinger) (*http.ServeMux, *bytes.Buffer) {
	log, buf := newLogger()
	mux := http.NewServeMux()
	httpapi.Register(mux, httpapi.Deps{DB: db, Log: log})
	return mux, buf
}

func TestHealthWhenTheDatabaseAnswers(t *testing.T) {
	db := &pinger{}
	mux, buf := newMux(db)
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
	mux, buf := newMux(&pinger{err: errors.New("connection refused")})
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
	httpapi.RegisterAdmin(mux)
	for _, path := range []string{"/api/v1/admin/", "/api/v1/admin/no-such-endpoint", "/api/v1/admin/a/b"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			w := serve(mux, httptest.NewRequest(method, path, nil))
			assert.Equal(t, http.StatusNotFound, w.Code, path)
			code, _ := envelope(t, w.Body)
			assert.Equal(t, "not_found", code, path)
		}
	}
}

func TestForbidden(t *testing.T) {
	w := httptest.NewRecorder()
	httpapi.Forbidden(w, httptest.NewRequest(http.MethodPost, "/", nil))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	code, msg := envelope(t, w.Body)
	assert.Equal(t, "forbidden", code)
	assert.NotEmpty(t, msg)
}
