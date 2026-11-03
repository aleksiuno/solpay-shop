package httpapi_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/httpapi"
)

var nothing = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

func TestRequestIDIsANewVersion4UUID(t *testing.T) {
	h := httpapi.RequestID(nothing)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	client := uuid.NewV4().String()
	req.Header.Set("X-Request-ID", client)

	first := serve(h, req).Header().Get("X-Request-ID")
	second := serve(h, req).Header().Get("X-Request-ID")
	u, err := uuid.Parse(first)
	require.NoError(t, err)
	assert.Equal(t, byte(4), u[6]>>4, "version")
	assert.Equal(t, byte(0b10), u[8]>>6, "variant")
	assert.NotEqual(t, client, first, "the client's id is ignored")
	assert.NotEqual(t, first, second)
}

func TestAccessLog(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		status  int
		bytes   float64
	}{
		{"a handler that sends nothing", func(http.ResponseWriter, *http.Request) {}, http.StatusOK, 0},
		{"a body without a status", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "hello")
		}, http.StatusOK, 5},
		{"a status and a body", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = io.WriteString(w, "tea")
			_, _ = io.WriteString(w, "pot")
		}, http.StatusTeapot, 6},
		{"an informational status first", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusEarlyHints)
			w.WriteHeader(http.StatusCreated)
		}, http.StatusCreated, 0},
		{"switching protocols, which no other status follows", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusSwitchingProtocols)
		}, http.StatusSwitchingProtocols, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, buf := newLogger()
			h := httpapi.RequestID(httpapi.AccessLog(log)(tt.handler))
			w := serve(h, httptest.NewRequest(http.MethodPost, "/orders/1", nil))

			lines := logLines(t, buf)
			require.Len(t, lines, 1)
			l := lines[0]
			assert.Equal(t, "INFO", l["level"])
			assert.Equal(t, "request", l["msg"])
			assert.Equal(t, http.MethodPost, l["method"])
			assert.Equal(t, "/orders/1", l["path"])
			assert.InDelta(t, tt.status, l["status"], 0)
			assert.InDelta(t, tt.bytes, l["bytes"], 0)
			assert.Contains(t, l, "duration")
			assert.Equal(t, w.Header().Get("X-Request-ID"), l["request_id"])
		})
	}
}

func TestAccessLogLeavesOutTheQueryString(t *testing.T) {
	log, buf := newLogger()
	serve(httpapi.AccessLog(log)(nothing), httptest.NewRequest(http.MethodGet, "/d/1?exp=1700000000&sig=s3cr3t", nil))

	lines := logLines(t, buf)
	require.Len(t, lines, 1)
	assert.Equal(t, "/d/1", lines[0]["path"])
	for _, s := range []string{"?", "exp", "1700000000", "sig", "s3cr3t"} {
		assert.NotContains(t, buf.String(), s)
	}
}

func TestAccessLogKeepsFlushWorking(t *testing.T) {
	log, _ := newLogger()
	h := httpapi.AccessLog(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		assert.NoError(t, http.NewResponseController(w).Flush())
	}))
	w := serve(h, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.True(t, w.Flushed)
}

func TestRecoverAnswersAPanicWithTheInternalEnvelope(t *testing.T) {
	log, buf := newLogger()
	h := httpapi.RequestID(httpapi.AccessLog(log)(httpapi.Recover(log)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Disposition", "attachment")
			w.Header().Set("Content-Length", "1000")
			panic("boom")
		}))))
	w := serve(h, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.JSONEq(t, `{"error": {"code": "internal", "message": "internal error"}}`, w.Body.String())
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Empty(t, w.Header().Values("Content-Disposition"), "the handler's headers are dropped")
	assert.Empty(t, w.Header().Values("Content-Length"))
	id := w.Header().Get("X-Request-ID")
	assert.NotEmpty(t, id, "headers set before the handler ran are kept")

	lines := logLines(t, buf)
	require.Len(t, lines, 2)
	assert.Equal(t, "ERROR", lines[0]["level"])
	assert.Equal(t, "handler panicked", lines[0]["msg"])
	assert.Equal(t, "boom", lines[0]["panic"])
	assert.Equal(t, false, lines[0]["response_started"])
	assert.Contains(t, lines[0]["stack"], "middleware_test.go")
	assert.Equal(t, id, lines[0]["request_id"])
	assert.InDelta(t, http.StatusInternalServerError, lines[1]["status"], 0, "the access log sees the 500")
}

// The writer refuses a status outside 100 to 999 with a panic before it sends anything, so the
// response has not started and can still be the internal envelope.
func TestRecoverAnswersARefusedStatusWithTheInternalEnvelope(t *testing.T) {
	log, buf := newLogger()
	h := httpapi.AccessLog(log)(httpapi.Recover(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(1000)
	})))
	w := serve(h, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	lines := logLines(t, buf)
	require.Len(t, lines, 2)
	assert.Equal(t, false, lines[0]["response_started"])
	assert.InDelta(t, http.StatusInternalServerError, lines[1]["status"], 0)
}

func TestRecoverAbortsAResponseThatHasStarted(t *testing.T) {
	tests := []struct {
		name  string
		start func(w http.ResponseWriter)
		body  string
	}{
		{"a body", func(w http.ResponseWriter) { _, _ = io.WriteString(w, "partial") }, "partial"},
		{"a status only", func(w http.ResponseWriter) { w.WriteHeader(http.StatusCreated) }, ""},
		{"a flush", func(w http.ResponseWriter) { _ = http.NewResponseController(w).Flush() }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, buf := newLogger()
			h := httpapi.RequestID(httpapi.AccessLog(log)(httpapi.Recover(log)(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					tt.start(w)
					panic("boom")
				}))))
			w := httptest.NewRecorder()

			assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
				h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
			}, "net/http closes the connection on this panic")
			assert.Equal(t, tt.body, w.Body.String(), "no envelope after the start")
			lines := logLines(t, buf)
			require.Len(t, lines, 2)
			assert.Equal(t, "handler panicked", lines[0]["msg"])
			assert.Equal(t, true, lines[0]["response_started"])
			assert.Equal(t, "request", lines[1]["msg"], "the aborted request is logged too")
		})
	}
}

func TestRecoverPassesADeliberateAbortOn(t *testing.T) {
	for _, abort := range []error{http.ErrAbortHandler, fmt.Errorf("copy: %w", http.ErrAbortHandler)} {
		log, buf := newLogger()
		h := httpapi.Recover(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(abort)
		}))
		assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		}, "the bare sentinel, which net/http does not log")
		assert.Empty(t, buf.String(), "not logged as a panic")
	}
}

func TestRecoverLeavesAResponseWithoutPanicAlone(t *testing.T) {
	log, buf := newLogger()
	h := httpapi.Recover(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	w := serve(h, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Empty(t, buf.String())
}
