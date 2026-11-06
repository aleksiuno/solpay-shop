package httpapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		{"a copy without a status", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.Copy(w, io.LimitReader(strings.NewReader("hello, world"), 5))
		}, http.StatusOK, 5},
		{"an empty copy, then a status", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.Copy(w, io.LimitReader(strings.NewReader(""), 0))
			w.WriteHeader(http.StatusNotFound)
		}, http.StatusNotFound, 0},
		{"a status, then an empty copy", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.Copy(w, io.LimitReader(strings.NewReader(""), 0))
		}, http.StatusAccepted, 0},
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

// readerFrom is a writer with its own ReadFrom, as the server's is. It notes whether a copy
// reached it.
type readerFrom struct {
	*httptest.ResponseRecorder
	copied bool
}

func (w *readerFrom) ReadFrom(src io.Reader) (int64, error) {
	w.copied = true
	return io.Copy(w.ResponseRecorder, src)
}

// A copy behind both recorders, as http.ServeContent makes one, reaches the writer's own ReadFrom,
// which sends a file by sendfile, and the access log still counts it.
func TestAccessLogKeepsReadFromWorking(t *testing.T) {
	log, buf := newLogger()
	h := httpapi.AccessLog(log)(httpapi.Recover(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
		_, err := io.Copy(w, io.LimitReader(strings.NewReader("the checklist"), 9))
		assert.NoError(t, err)
	})))
	w := &readerFrom{ResponseRecorder: httptest.NewRecorder()}
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/d/1", nil))

	assert.True(t, w.copied)
	assert.Equal(t, "the check", w.Body.String())
	lines := logLines(t, buf)
	require.Len(t, lines, 1)
	assert.InDelta(t, http.StatusPartialContent, lines[0]["status"], 0)
	assert.InDelta(t, 9, lines[0]["bytes"], 0)
}

// A handler behind both recorders can still set its own deadlines, as the download handler does.
// Only a real server's writer takes them: a test's recorder refuses every deadline.
func TestAccessLogAndRecoverPassDeadlinesOn(t *testing.T) {
	log, _ := newLogger()
	errs := make(chan error, 2)
	srv := httptest.NewServer(httpapi.AccessLog(log)(httpapi.Recover(log)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			rc := http.NewResponseController(w)
			errs <- rc.SetWriteDeadline(time.Now().Add(time.Minute))
			errs <- rc.SetReadDeadline(time.Now().Add(time.Minute))
		}))))
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.NoError(t, <-errs, "write deadline")
	assert.NoError(t, <-errs, "read deadline")
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

// A copy that copies nothing sends nothing, so the response has not started and a panic after it
// can still be the internal envelope.
func TestRecoverAnswersAPanicAfterAnEmptyCopyWithTheInternalEnvelope(t *testing.T) {
	log, buf := newLogger()
	h := httpapi.AccessLog(log)(httpapi.Recover(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.Copy(w, io.LimitReader(strings.NewReader(""), 0))
		panic("boom")
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
		{"part of a copy", func(w http.ResponseWriter) { _, _ = io.Copy(w, &failingReader{text: "partial"}) }, "partial"},
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

// failingReader gives its text and then panics, as a source that fails part-way through a copy.
type failingReader struct{ text string }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.text == "" {
		panic("boom")
	}
	n := copy(p, r.text)
	r.text = r.text[n:]
	return n, nil
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

func TestBasicAuth(t *testing.T) {
	const user, pass = "admin", "test-only-password"
	tests := []struct {
		name    string
		prepare func(r *http.Request)
		ok      bool
	}{
		{"no credentials", func(*http.Request) {}, false},
		{"a wrong user", func(r *http.Request) { r.SetBasicAuth("root", pass) }, false},
		{"a wrong password", func(r *http.Request) { r.SetBasicAuth(user, "wrong-password") }, false},
		{"part of the password", func(r *http.Request) { r.SetBasicAuth(user, pass[:4]) }, false},
		{"the password with more after it", func(r *http.Request) { r.SetBasicAuth(user, pass+"x") }, false},
		{"user and password swapped", func(r *http.Request) { r.SetBasicAuth(pass, user) }, false},
		{"another scheme", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+pass) }, false},
		{"the right credentials", func(r *http.Request) { r.SetBasicAuth(user, pass) }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			h := httpapi.BasicAuth(user, pass)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusNoContent)
			}))
			r := httptest.NewRequest(http.MethodGet, "/admin/", nil)
			tt.prepare(r)
			w := serve(h, r)

			assert.Equal(t, tt.ok, reached)
			if tt.ok {
				assert.Equal(t, http.StatusNoContent, w.Code)
				assert.Empty(t, w.Header().Values("WWW-Authenticate"))
				return
			}
			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.Equal(t, `Basic realm="solpay-shop admin"`, w.Header().Get("WWW-Authenticate"))
			code, _ := envelope(t, w.Body)
			assert.Equal(t, "unauthorized", code)
		})
	}
}

// A request without credentials is refused even when the expected ones are empty, which r.BasicAuth
// also returns for a request that sends none.
func TestBasicAuthRefusesNoCredentialsEvenWhenEmptyAreExpected(t *testing.T) {
	h := httpapi.BasicAuth("", "")(nothing)
	w := serve(h, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// envelope decodes a JSON error envelope.
func envelope(t *testing.T, body io.Reader) (code, message string) {
	t.Helper()
	var e struct {
		Error struct{ Code, Message string }
	}
	require.NoError(t, json.NewDecoder(body).Decode(&e))
	return e.Error.Code, e.Error.Message
}
