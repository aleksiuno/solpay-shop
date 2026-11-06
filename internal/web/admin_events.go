package web

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/aleksiuno/solpay-shop/internal/audit"
)

// eventPage is what the admin's event log shows: one page of events, the links to the newest
// page and to the next older one, each "" when there is none, and what was wrong with the page
// asked for.
type eventPage struct {
	Events  []audit.Event
	PerPage int
	Newest  string
	Older   string
	Problem string
}

// adminEvents shows the newest events, a page at a time: those older than the event that
// ?before= names, when given. A page that names no event shows the problem: 400.
func (s site) adminEvents(w http.ResponseWriter, r *http.Request) {
	p := eventPage{PerPage: audit.PageSize}
	var f audit.ListFilter
	if v := r.URL.Query().Get("before"); v != "" {
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
		p.Newest = eventsPath(audit.ListFilter{})
	}
	// A full page may have older events after it; a shorter one is the last.
	if len(evs) == audit.PageSize {
		f.Before = evs[len(evs)-1].ID
		p.Older = eventsPath(f)
	}
	s.render(w, r, http.StatusOK, "admin_events.html", view{Title: "Events", Wide: true, Data: p})
}

// refuseFilter shows the event log with problem and no events: 400.
func (s site) refuseFilter(w http.ResponseWriter, r *http.Request, p eventPage, problem string) {
	p.Problem = problem
	s.render(w, r, http.StatusBadRequest, "admin_events.html", view{Title: "Events", Wide: true, Data: p})
}

// eventsPath returns the path of the event log page that f selects.
func eventsPath(f audit.ListFilter) string {
	q := url.Values{}
	if f.Before > 0 {
		q.Set("before", strconv.FormatInt(f.Before, 10))
	}
	if len(q) == 0 {
		return "/admin/events"
	}
	return "/admin/events?" + q.Encode()
}
