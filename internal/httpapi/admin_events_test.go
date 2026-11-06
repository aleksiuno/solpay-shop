package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/aleksiuno/solpay-shop/internal/audit"
)

// newEventsAPI returns the admin routes over e.
func newEventsAPI(e *eventLog) *http.ServeMux {
	return newOrderAdminAPI(&orderAdmin{}, ledger{}, e)
}

// fullPage returns a full page of events, the newest with id top.
func fullPage(top int64) []audit.Event {
	evs := make([]audit.Event, 0, audit.PageSize)
	for i := range int64(audit.PageSize) {
		evs = append(evs, audit.Event{ID: top - i, Kind: audit.PaymentSeen, Message: "payment seen", CreatedAt: now})
	}
	return evs
}

func TestAdminListsEvents(t *testing.T) {
	id := pending.ID
	e := &eventLog{events: []audit.Event{
		{ID: 2, Kind: audit.RPCError, Message: "RPC call failed", Data: map[string]any{"error": "a < b & c"},
			CreatedAt: now.Add(time.Minute).In(eet)},
		{ID: 1, OrderID: &id, Kind: audit.PaymentVerified, Message: "order paid", Signature: "sig-credited",
			Data: map[string]any{"lamports": json.Number("10000000")}, CreatedAt: now},
	}}
	w := get(newEventsAPI(e), "/api/v1/admin/events")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, audit.ListFilter{}, e.filter, "every event")
	assert.JSONEq(t, `{"events": [
		{"id": 2, "order_id": null, "level": "error", "kind": "rpc.error", "message": "RPC call failed",
		 "signature": "", "data": {"error": "a < b & c"}, "created_at": "2026-11-03T12:01:00Z"},
		{"id": 1, "order_id": "3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e", "level": "info", "kind": "payment.verified",
		 "message": "order paid", "signature": "sig-credited", "data": {"lamports": 10000000},
		 "created_at": "2026-11-03T12:00:00Z"}],
		"next_before": null}`, w.Body.String())
	assert.Contains(t, w.Body.String(), `"a < b & c"`, "not escaped for HTML")

	w = get(newEventsAPI(&eventLog{}), "/api/v1/admin/events")
	assert.JSONEq(t, `{"events": [], "next_before": null}`, w.Body.String())
}

// A full page gives the id to ask the next page with; a shorter one gives none.
func TestAdminListEventsPages(t *testing.T) {
	e := &eventLog{events: fullPage(250)}
	w := get(newEventsAPI(e), "/api/v1/admin/events?before=251")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, audit.ListFilter{Before: 251}, e.filter)
	var page struct {
		Events     []json.RawMessage `json:"events"`
		NextBefore *int64            `json:"next_before"`
	}
	if assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &page)) && assert.NotNil(t, page.NextBefore) {
		assert.Len(t, page.Events, audit.PageSize)
		assert.Equal(t, int64(151), *page.NextBefore)
	}

	e = &eventLog{events: fullPage(250)[:audit.PageSize-1]}
	assert.Contains(t, get(newEventsAPI(e), "/api/v1/admin/events").Body.String(), `"next_before":null`, "the last page")

	// With 100 events, the first page is full and the next starts before the event with id 1.
	e = &eventLog{}
	w = get(newEventsAPI(e), "/api/v1/admin/events?before=1")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, audit.ListFilter{Before: 1}, e.filter)
	assert.JSONEq(t, `{"events": [], "next_before": null}`, w.Body.String())
}

func TestAdminListEventsByOrderAndLevel(t *testing.T) {
	id := pending.ID
	for _, l := range []audit.Level{audit.Info, audit.Warn, audit.Error} {
		e := &eventLog{}
		// An id in capitals is the same id.
		w := get(newEventsAPI(e), "/api/v1/admin/events?order_id="+strings.ToUpper(id.String())+"&level="+string(l)+"&before=300")
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, audit.ListFilter{OrderID: &id, Level: l, Before: 300}, e.filter)
	}
}

func TestAdminListEventsRefusesABadFilter(t *testing.T) {
	tests := []struct{ query, message string }{
		{"order_id=not-a-uuid", "order_id must be a UUID"},
		{"level=debug", "level must be one of info, warn, error"},
		{"level=WARN", "level must be one of info, warn, error"},
		{"before=0", "before must be a positive integer"},
		{"before=-5", "before must be a positive integer"},
		{"before=1.5", "before must be a positive integer"},
		{"before=9223372036854775808", "before must be a positive integer"},
	}
	for _, tt := range tests {
		// A store that fails gives a 500, so a 400 is refused before the store is asked.
		w := get(newEventsAPI(&eventLog{err: errBoom}), "/api/v1/admin/events?"+tt.query)
		assert.Equal(t, http.StatusBadRequest, w.Code, tt.query)
		code, msg := envelope(t, w.Body)
		assert.Equal(t, "invalid_request", code, tt.query)
		assert.Equal(t, tt.message, msg, tt.query)
	}
}

func TestAdminListEventsWhenTheStoreFails(t *testing.T) {
	w := get(newEventsAPI(&eventLog{err: errBoom}), "/api/v1/admin/events")
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	code, _ := envelope(t, w.Body)
	assert.Equal(t, "internal", code)
}
