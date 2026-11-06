package httpapi

import (
	"net/http"
	"slices"
	"strconv"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/audit"
)

// eventLevels are the levels that ?level= may name.
var eventLevels = []audit.Level{audit.Info, audit.Warn, audit.Error}

// listAdminEvents answers with a page of events, the newest first: those older than the event
// that ?before= names, of the order that ?order_id= names and at the level that ?level= names,
// each when given. {"events": [...], "next_before": 123}: next_before is the last event's id
// when the page is full, to pass as before for the next page, and null otherwise.
func (a api) listAdminEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f audit.ListFilter
	if v := q.Get("order_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "order_id must be a UUID")
			return
		}
		f.OrderID = &id
	}
	f.Level = audit.Level(q.Get("level"))
	if f.Level != "" && !slices.Contains(eventLevels, f.Level) {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "level must be one of info, warn, error")
		return
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "before must be a positive integer")
			return
		}
		f.Before = n
	}
	evs, err := a.EventLog.List(r.Context(), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := struct {
		Events     []eventJSON `json:"events"`
		NextBefore *int64      `json:"next_before"`
	}{Events: make([]eventJSON, 0, len(evs))}
	for _, ev := range evs {
		out.Events = append(out.Events, newEventJSON(ev))
	}
	if len(evs) == audit.PageSize {
		out.NextBefore = new(evs[len(evs)-1].ID)
	}
	writeJSON(w, http.StatusOK, out)
}
