package httpapi_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/httpapi"
)

// newLogger returns a JSON logger behind a ContextHandler, and the buffer it writes to.
func newLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(httpapi.ContextHandler{Handler: slog.NewJSONHandler(&buf, nil)}), &buf
}

// logLines parses each JSON line in buf. Each line may have at most one request_id key: a second
// one would be lost when the line is parsed into a map.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(buf.String()) {
		require.LessOrEqual(t, strings.Count(line, `"request_id":`), 1, "one request_id key in %s", line)
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m), line)
		out = append(out, m)
	}
	return out
}

// serve sends one request to h and returns the response.
func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestContextHandlerAddsTheRequestID(t *testing.T) {
	log, buf := newLogger()
	h := httpapi.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		log.InfoContext(r.Context(), "plain")
		log.With(slog.String("order_id", "o1")).InfoContext(r.Context(), "with")
		log.WithGroup("g").InfoContext(r.Context(), "group", slog.String("k", "v"))
	}))
	w := serve(h, httptest.NewRequest(http.MethodGet, "/", nil))
	log.InfoContext(t.Context(), "outside a request")

	id := w.Header().Get("X-Request-ID")
	lines := logLines(t, buf)
	require.Len(t, lines, 4)
	assert.Equal(t, id, lines[0]["request_id"])
	assert.Equal(t, id, lines[1]["request_id"])
	assert.Equal(t, "o1", lines[1]["order_id"])
	assert.Equal(t, map[string]any{"k": "v", "request_id": id}, lines[2]["g"], "inside the logger's group")
	assert.NotContains(t, lines[3], "request_id")
}
