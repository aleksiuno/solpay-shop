package audit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/audit"
)

// store records each InsertEvents call and returns err.
type store struct {
	calls [][]audit.Event
	ctxs  []context.Context
	err   error
}

func (s *store) InsertEvents(ctx context.Context, evs ...audit.Event) error {
	s.calls = append(s.calls, evs)
	s.ctxs = append(s.ctxs, ctx)
	return s.err
}

func newRecorder(s audit.Store) (*audit.Recorder, *bytes.Buffer) {
	var buf bytes.Buffer
	return audit.NewRecorder(s, slog.New(slog.NewJSONHandler(&buf, nil))), &buf
}

type ctxKey struct{}

// ctxHandler notes the ctxKey value of the context that each log line is written with.
type ctxHandler struct {
	slog.Handler
	values *[]any
}

func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	*h.values = append(*h.values, ctx.Value(ctxKey{}))
	return h.Handler.Handle(ctx, r)
}

// newRecorderNotingContexts is newRecorder, but it also returns the ctxKey value of each line's
// context, so a test can check that the caller's context reaches the log handler.
func newRecorderNotingContexts(s audit.Store) (*audit.Recorder, *bytes.Buffer, *[]any) {
	var buf bytes.Buffer
	var values []any
	h := ctxHandler{Handler: slog.NewJSONHandler(&buf, nil), values: &values}
	return audit.NewRecorder(s, slog.New(h)), &buf, &values
}

// logLines parses each JSON line in buf, with numbers as json.Number. Each line must have exactly
// one level key: a second one would be lost when the line is parsed into a map.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(buf.String()) {
		require.Equal(t, 1, strings.Count(line, `"level":`), "one level key in %s", line)
		d := json.NewDecoder(strings.NewReader(line))
		d.UseNumber()
		var m map[string]any
		require.NoError(t, d.Decode(&m), line)
		out = append(out, m)
	}
	return out
}

func TestLogWritesTheEvent(t *testing.T) {
	r, buf := newRecorder(&store{})
	id := uuid.NewV4()
	r.Log(t.Context(), audit.Event{
		OrderID:   &id,
		Kind:      audit.PaymentOverpaid,
		Message:   "order received more than its amount",
		Signature: "sig-1",
		Data:      map[string]any{"surplus_lamports": 500},
	})
	lines := logLines(t, buf)
	require.Len(t, lines, 1)
	line := lines[0]
	assert.ElementsMatch(t,
		[]string{"time", "level", "msg", "kind", "component", "order_id", "signature", "data"},
		slices.Collect(maps.Keys(line)))
	assert.Equal(t, "WARN", line["level"])
	assert.Equal(t, "order received more than its amount", line["msg"])
	assert.Equal(t, "payment.overpaid", line["kind"])
	assert.Equal(t, "payment", line["component"])
	assert.Equal(t, id.String(), line["order_id"])
	assert.Equal(t, "sig-1", line["signature"])
	assert.Equal(t, map[string]any{"surplus_lamports": json.Number("500")}, line["data"])
}

func TestLogLeavesOutUnsetFields(t *testing.T) {
	r, buf := newRecorder(&store{})
	r.Log(t.Context(), audit.Event{
		Kind:    audit.WatcherStarted,
		Message: "watcher started",
		Data:    map[string]any{},
	})
	lines := logLines(t, buf)
	require.Len(t, lines, 1)
	assert.ElementsMatch(t,
		[]string{"time", "level", "msg", "kind", "component"},
		slices.Collect(maps.Keys(lines[0])))
}

func TestLogLevelAndComponentOfEachKind(t *testing.T) {
	slogLevel := map[audit.Level]string{audit.Info: "INFO", audit.Warn: "WARN", audit.Error: "ERROR"}
	component := map[string]string{ // by the prefix of the kind
		"order":    "order",
		"payment":  "payment",
		"rpc":      "payment",
		"watcher":  "payment",
		"download": "download",
	}
	for _, k := range kinds {
		r, buf := newRecorder(&store{})
		r.Log(t.Context(), audit.Event{Kind: k.kind, Message: "m"})
		lines := logLines(t, buf)
		require.Len(t, lines, 1)
		prefix, _, _ := strings.Cut(k.value, ".")
		assert.Equal(t, slogLevel[k.level], lines[0]["level"], "%s", k.kind)
		assert.Equal(t, component[prefix], lines[0]["component"], "%s", k.kind)
	}

	r, buf := newRecorder(&store{})
	r.Log(t.Context(), audit.Event{Kind: "payment.refunded", Message: "m"})
	lines := logLines(t, buf)
	require.Len(t, lines, 1)
	assert.Equal(t, "ERROR", lines[0]["level"], "an unknown kind")
	assert.Equal(t, "unknown", lines[0]["component"], "an unknown kind")
}

func TestLogDoesNotStore(t *testing.T) {
	s := &store{}
	r, _ := newRecorder(s)
	r.Log(t.Context(), audit.Event{Kind: audit.OrderCreated, Message: "order created"})
	assert.Empty(t, s.calls)
}

func TestRecordStoresThenLogs(t *testing.T) {
	s := &store{}
	r, buf, values := newRecorderNotingContexts(s)
	ctx := context.WithValue(t.Context(), ctxKey{}, "request")
	evs := []audit.Event{
		{Kind: audit.PaymentSeen, Message: "payment seen", Signature: "sig-1"},
		{Kind: audit.PaymentVerified, Message: "order paid", Signature: "sig-1"},
	}
	r.Record(ctx, evs...)

	require.Len(t, s.calls, 1, "one store call for all the events")
	assert.Equal(t, evs, s.calls[0])
	assert.Equal(t, "request", s.ctxs[0].Value(ctxKey{}), "the caller's context")
	lines := logLines(t, buf)
	require.Len(t, lines, 2)
	assert.Equal(t, "payment.seen", lines[0]["kind"])
	assert.Equal(t, "payment.verified", lines[1]["kind"])
	assert.Equal(t, []any{"request", "request"}, *values, "each line is written with the caller's context")
}

func TestRecordLogsTheEventsWhenTheStoreFails(t *testing.T) {
	r, buf, values := newRecorderNotingContexts(&store{err: errors.New("connection refused")})
	r.Record(context.WithValue(t.Context(), ctxKey{}, "request"),
		audit.Event{Kind: audit.RPCError, Message: "getSignaturesForAddress failed"},
		audit.Event{Kind: audit.RPCTimeout, Message: "getTransaction timed out"},
	)
	lines := logLines(t, buf)
	require.Len(t, lines, 3)
	assert.Equal(t, "ERROR", lines[0]["level"])
	assert.Equal(t, "events not stored", lines[0]["msg"])
	assert.Equal(t, "connection refused", lines[0]["error"])
	assert.Equal(t, json.Number("2"), lines[0]["events"])
	assert.Equal(t, "rpc.error", lines[1]["kind"])
	assert.Equal(t, "rpc.timeout", lines[2]["kind"])
	assert.Equal(t, []any{"request", "request", "request"}, *values,
		"the failure line and each event line are written with the caller's context")
}

func TestRecordNothing(t *testing.T) {
	s := &store{}
	r, buf := newRecorder(s)
	r.Record(t.Context())
	assert.Empty(t, s.calls)
	assert.Empty(t, buf.String())
}
