package web

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/audit"
)

// levels are the event levels, in the order the admin's filter offers them.
var levels = []audit.Level{audit.Info, audit.Warn, audit.Error}

// eventPage is what the admin's event log shows: one page of events, the filter, the links to
// the newest page and to the next older one, each "" when there is none, and what was wrong with
// the filter.
type eventPage struct {
	Events  []audit.Event
	OrderID string // the filter's order id, or ""
	Level   audit.Level
	Levels  []audit.Level
	PerPage int
	Newest  string
	Older   string
	Problem string
}

// adminEvents shows the newest events, a page at a time: those older than the event that
// ?before= names, of the order that ?order_id= names and at the level that ?level= names, each
// when given. A filter that names none of these shows the page with the problem: 400.
func (s site) adminEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := eventPage{OrderID: strings.TrimSpace(q.Get("order_id")), Level: audit.Level(q.Get("level")), Levels: levels, PerPage: audit.PageSize}
	f := audit.ListFilter{Level: p.Level}
	if p.OrderID != "" {
		id, err := uuid.Parse(p.OrderID)
		if err != nil {
			s.refuseFilter(w, r, p, "The order id is not a UUID.")
			return
		}
		f.OrderID, p.OrderID = &id, id.String()
	}
	if f.Level != "" && !slices.Contains(levels, f.Level) {
		s.refuseFilter(w, r, p, "There is no such level.")
		return
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			s.refuseFilter(w, r, p, "There is no such page.")
			return
		}
		f.Before = n
	}
	evs, err := s.EventLog.List(r.Context(), f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p.Events = evs
	if f.Before > 0 {
		p.Newest = eventsPath(audit.ListFilter{OrderID: f.OrderID, Level: f.Level})
	}
	// A full page may have older events after it; a shorter one is the last.
	if len(evs) == audit.PageSize {
		f.Before = evs[len(evs)-1].ID
		p.Older = eventsPath(f)
	}
	s.render(w, r, http.StatusOK, "admin_events.html", view{Title: "Events", Wide: true, Data: p})
}

// refuseFilter shows the event log's filter as sent, with problem and no events: 400.
func (s site) refuseFilter(w http.ResponseWriter, r *http.Request, p eventPage, problem string) {
	p.Problem = problem
	s.render(w, r, http.StatusBadRequest, "admin_events.html", view{Title: "Events", Wide: true, Data: p})
}

// eventsPath returns the path of the event log page that f selects.
func eventsPath(f audit.ListFilter) string {
	q := url.Values{}
	if f.OrderID != nil {
		q.Set("order_id", f.OrderID.String())
	}
	if f.Level != "" {
		q.Set("level", string(f.Level))
	}
	if f.Before > 0 {
		q.Set("before", strconv.FormatInt(f.Before, 10))
	}
	if len(q) == 0 {
		return "/admin/events"
	}
	return "/admin/events?" + q.Encode()
}
