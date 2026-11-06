package web_test

import (
	"encoding/json"
	"html"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/web"
)

// adminEvents returns the admin pages over e.
func adminEvents(e *eventLog) *http.ServeMux {
	return adminOver(web.Deps{EventLog: e})
}

// fullPage returns a full page of events, the newest with id top.
func fullPage(top int64) []audit.Event {
	evs := make([]audit.Event, 0, audit.PageSize)
	for i := range int64(audit.PageSize) {
		evs = append(evs, audit.Event{ID: top - i, Kind: audit.PaymentSeen, Message: "payment seen", CreatedAt: deadline})
	}
	return evs
}

// The log shows each event with its level, its order, its message and its details, a warn row
// amber and an error row red.
func TestAdminEventsLog(t *testing.T) {
	id := pending.ID
	e := &eventLog{events: []audit.Event{
		{ID: 3, Kind: audit.RPCError, Message: "RPC call failed", Data: map[string]any{"method": "getSignaturesForAddress", "error": "a < b & c"},
			CreatedAt: deadline.Add(time.Minute)},
		{ID: 2, OrderID: &id, Kind: audit.PaymentUnderpaid, Message: "payment short", Signature: "sig-underpaid",
			Data: map[string]any{"lamports": json.Number("4000000")}, CreatedAt: deadline.Add(-time.Minute).In(time.FixedZone("EET", 2*60*60))},
		{ID: 1, OrderID: &id, Kind: audit.OrderCreated, Message: "order created", CreatedAt: deadline.Add(-15 * time.Minute)},
	}}
	body := requirePage(t, get(adminEvents(e), "/admin/events"), http.StatusOK)

	assert.Equal(t, audit.ListFilter{}, e.filter, "every event")
	// The order's id is shortened to its first 8 characters, the whole id in the title and links.
	orderCell := `<td><a href="/admin/events?order_id=` + id.String() + `" title="` + id.String() + `"><code>3c9e6f1a</code></a>` +
		` (<a href="/admin/orders/` + id.String() + `">order</a>)</td>`
	for _, want := range []string{
		"<title>Events · solpay-shop</title>",
		`<body class="wide">`, // the full width, for the table
		`<tr class="lvl-error"><td>2026-11-05 12:11:00 UTC</td><td>error</td><td><code>rpc.error</code></td><td></td><td>RPC call failed</td>` +
			`<td><code>` + html.EscapeString(`{"error":"a < b & c","method":"getSignaturesForAddress"}`) + `</code></td></tr>`,
		`<tr class="lvl-warn"><td>2026-11-05 12:09:00 UTC</td><td>warn</td><td><code>payment.underpaid</code></td>` + orderCell +
			`<td>payment short</td><td><a href="https://explorer.solana.com/tx/sig-underpaid?cluster=devnet" title="sig-underpaid"><code>sig-unde</code></a> ` +
			`<code>` + html.EscapeString(`{"lamports":4000000}`) + `</code></td></tr>`,
		`<tr class="lvl-info"><td>2026-11-05 11:55:00 UTC</td><td>info</td><td><code>order.created</code></td>` + orderCell +
			`<td>order created</td><td></td></tr>`,
		`<form class="filter" method="get" action="/admin/events">`,
		`<input id="order_id" name="order_id" value="" size="36" spellcheck="false">`,
		`<select id="level" name="level">
<option value="">All</option>
<option>info</option>
<option>warn</option>
<option>error</option>
</select>`,
		`<p class="notice">Newest first, at most 100 events a page.</p>`,
		`<p><a href="/admin/orders">All orders</a></p>`,
	} {
		assert.Contains(t, body, want)
	}
	assert.NotContains(t, body, "← Newest", "the first page")
	assert.NotContains(t, body, "Older →", "not a full page")
	assert.NotContains(t, body, "<p></p>")
}

func TestAdminEventsWithNone(t *testing.T) {
	assert.Contains(t, requirePage(t, get(adminEvents(&eventLog{}), "/admin/events"), http.StatusOK), `<tr><td colspan="6">No events.</td></tr>`)
}

// A full page links to the next older one, which starts after its last event, and a later page
// links back to the newest.
func TestAdminEventsPages(t *testing.T) {
	e := &eventLog{events: fullPage(250)}
	body := requirePage(t, get(adminEvents(e), "/admin/events"), http.StatusOK)
	assert.Contains(t, body, `<p><a href="/admin/events?before=151">Older →</a></p>`)
	assert.NotContains(t, body, "← Newest")

	e = &eventLog{events: fullPage(150)}
	body = requirePage(t, get(adminEvents(e), "/admin/events?before=151"), http.StatusOK)
	assert.Equal(t, audit.ListFilter{Before: 151}, e.filter)
	assert.Contains(t, body, `<p><a href="/admin/events">← Newest</a> · <a href="/admin/events?before=51">Older →</a></p>`)

	e = &eventLog{events: fullPage(150)[:audit.PageSize-1]}
	body = requirePage(t, get(adminEvents(e), "/admin/events?before=151"), http.StatusOK)
	assert.Contains(t, body, `<p><a href="/admin/events">← Newest</a></p>`, "a page that is not full is the last")

	// With 100 events, the first page is full and the next starts before the event with id 1.
	e = &eventLog{}
	body = requirePage(t, get(adminEvents(e), "/admin/events?before=1"), http.StatusOK)
	assert.Equal(t, audit.ListFilter{Before: 1}, e.filter)
	assert.Contains(t, body, `<p><a href="/admin/events">← Newest</a></p>`)
	assert.Contains(t, body, "No events.")
}

// The filter is the order and the level asked for, and the page links keep it.
func TestAdminEventsByOrderAndLevel(t *testing.T) {
	id := pending.ID
	for _, l := range audit.Levels() {
		e := &eventLog{events: fullPage(250)}
		// An id in capitals, or pasted with spaces around it, is the same id.
		body := requirePage(t, get(adminEvents(e), "/admin/events?order_id=+"+strings.ToUpper(id.String())+"+&level="+string(l)+"&before=300"), http.StatusOK)
		assert.Equal(t, audit.ListFilter{OrderID: &id, Level: l, Before: 300}, e.filter)
		assert.Contains(t, body, `<input id="order_id" name="order_id" value="`+id.String()+`"`)
		for _, other := range audit.Levels() {
			assert.Equal(t, other == l, strings.Contains(body, "<option selected>"+string(other)+"</option>"), "%s selected for %s", other, l)
		}
		assert.Contains(t, body, `<a href="/admin/events?level=`+string(l)+`&amp;order_id=`+id.String()+`">← Newest</a>`)
		assert.Contains(t, body, `<a href="/admin/events?before=151&amp;level=`+string(l)+`&amp;order_id=`+id.String()+`">Older →</a>`)
	}

	// A filtered first page has no link to the newest: it is the newest.
	e := &eventLog{events: fullPage(250)}
	body := requirePage(t, get(adminEvents(e), "/admin/events?order_id="+id.String()+"&level=warn"), http.StatusOK)
	assert.Equal(t, audit.ListFilter{OrderID: &id, Level: audit.Warn}, e.filter)
	assert.NotContains(t, body, "← Newest")
	assert.Contains(t, body, `<p><a href="/admin/events?before=151&amp;level=warn&amp;order_id=`+id.String()+`">Older →</a></p>`)
}

// A filter that names nothing shows the page with the problem and the filter as sent, and no
// events.
func TestAdminEventsRefusesABadFilter(t *testing.T) {
	tests := []struct{ query, problem, orderID string }{
		{"order_id=not-a-uuid", "The order id is not a UUID.", "not-a-uuid"},
		{"level=debug", "There is no such level.", ""},
		{"level=WARN", "There is no such level.", ""},
		{"before=0", "There is no such page.", ""},
		{"before=-5", "There is no such page.", ""},
		{"before=next", "There is no such page.", ""},
		{"before=9223372036854775808", "There is no such page.", ""},
		{"order_id=" + pending.ID.String() + "&before=1.5", "There is no such page.", pending.ID.String()},
	}
	for _, tt := range tests {
		// A store that fails gives a 500, so a 400 is refused before the store is asked.
		body := requirePage(t, get(adminEvents(&eventLog{err: errBoom}), "/admin/events?"+tt.query), http.StatusBadRequest)
		assert.Contains(t, body, `<p class="problem" role="alert">`+tt.problem+`</p>`, tt.query)
		assert.Contains(t, body, `<input id="order_id" name="order_id" value="`+tt.orderID+`"`, tt.query)
		assert.NotContains(t, body, "<table", tt.query)
		assert.Contains(t, body, `<p><a href="/admin/orders">All orders</a></p>`, tt.query)
		assert.Contains(t, body, `<body class="wide">`, tt.query)
	}
}

func TestAdminEventsWhenTheStoreFails(t *testing.T) {
	requirePage(t, get(adminEvents(&eventLog{err: errBoom}), "/admin/events"), http.StatusInternalServerError)
}

// The order list links to the log, and an order's page to its events there.
func TestAdminOrderPagesLinkToTheLog(t *testing.T) {
	mux := adminOrders(&orderAdmin{orders: []order.Order{pending}}, ledger{}, &eventLog{})
	body := requirePage(t, get(mux, "/admin/orders"), http.StatusOK)
	assert.Contains(t, body, `<a href="/admin/events">The event log</a>`)
	assert.Contains(t, body, "<body>", "only the event log takes the full width")
	assert.Contains(t, requirePage(t, get(mux, "/admin/orders/"+pending.ID.String()), http.StatusOK),
		`<a href="/admin/events?order_id=`+pending.ID.String()+`">The order in the event log</a>`)
}
