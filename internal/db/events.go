package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/db/sqlcdb"
)

// EventsPageSize is the most events that Events.List returns at once.
const EventsPageSize = 100

// Events saves and lists the payment event log. It implements audit.Store.
type Events struct {
	pool *pgxpool.Pool
	q    *sqlcdb.Queries
}

var _ audit.Store = (*Events)(nil)

// NewEvents returns an Events that uses pool.
func NewEvents(pool *pgxpool.Pool) *Events {
	return &Events{pool: pool, q: sqlcdb.New(pool)}
}

// InsertEvents saves evs in one transaction, so either all of them are saved or none.
func (e *Events) InsertEvents(ctx context.Context, evs ...audit.Event) error {
	if len(evs) == 0 {
		return nil
	}
	return inTx(ctx, e.pool, func(q *sqlcdb.Queries) error {
		return insertEvents(ctx, q, evs)
	})
}

// insertEvents saves evs through q. When q is bound to a caller's transaction, the events commit or
// roll back with that transaction's other changes.
func insertEvents(ctx context.Context, q *sqlcdb.Queries, evs []audit.Event) error {
	for _, ev := range evs {
		var data []byte
		if len(ev.Data) > 0 {
			var err error
			if data, err = json.Marshal(ev.Data); err != nil {
				return fmt.Errorf("encode the data of a %s event: %w", ev.Kind, err)
			}
		}
		var signature *string
		if ev.Signature != "" {
			signature = &ev.Signature
		}
		err := q.InsertEvent(ctx, sqlcdb.InsertEventParams{
			OrderID:   ev.OrderID,
			Level:     string(ev.Kind.Level()),
			Kind:      string(ev.Kind),
			Message:   ev.Message,
			Signature: signature,
			Data:      data,
		})
		if err != nil {
			return fmt.Errorf("insert a %s event: %w", ev.Kind, err)
		}
	}
	return nil
}

// List returns up to EventsPageSize events that match f, newest first. Numbers in an event's data
// come back as json.Number, so a lamport amount keeps every digit.
func (e *Events) List(ctx context.Context, f audit.ListFilter) ([]audit.Event, error) {
	p := sqlcdb.ListEventsParams{OrderID: f.OrderID, PageSize: EventsPageSize}
	if f.Level != "" {
		level := string(f.Level)
		p.Level = &level
	}
	if f.Before > 0 {
		p.Before = &f.Before
	}
	rows, err := e.q.ListEvents(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	evs := make([]audit.Event, 0, len(rows))
	for _, r := range rows {
		ev := audit.Event{
			ID:        r.ID,
			OrderID:   r.OrderID,
			Kind:      audit.Kind(r.Kind),
			Message:   r.Message,
			CreatedAt: r.CreatedAt,
		}
		if r.Signature != nil {
			ev.Signature = *r.Signature
		}
		if r.Data != nil {
			d := json.NewDecoder(bytes.NewReader(r.Data))
			d.UseNumber()
			if err := d.Decode(&ev.Data); err != nil {
				return nil, fmt.Errorf("decode the data of event %d: %w", r.ID, err)
			}
		}
		evs = append(evs, ev)
	}
	return evs, nil
}
