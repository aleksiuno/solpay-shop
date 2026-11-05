package payment_test

import (
	"context"
	"slices"
	"time"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
)

// memStore keeps orders with their next poll times, the payment ledger and the events in memory,
// by the rules that db.Payments follows, and it is the event store of the watcher's Recorder too.
// Every order id it is given must be one it holds. fail makes the next call of a method, by name,
// fail. It is not safe for concurrent use.
type memStore struct {
	orders []*polled // in the order they were added
	ledger map[string]payment.Payment
	events []audit.Event
	fail   map[string]error
}

// polled is an order and when it is next due for a poll; nil means never.
type polled struct {
	order.Order
	next *time.Time
}

func newMemStore() *memStore {
	return &memStore{ledger: map[string]payment.Payment{}, fail: map[string]error{}}
}

// add stores o, due for a poll at its creation time, as an order is created.
func (s *memStore) add(o order.Order) {
	s.orders = append(s.orders, &polled{Order: o, next: new(o.CreatedAt)})
}

// get returns the order with id and its next poll time.
func (s *memStore) get(id uuid.UUID) polled {
	return *s.find(id)
}

func (s *memStore) find(id uuid.UUID) *polled {
	return s.orders[slices.IndexFunc(s.orders, func(p *polled) bool { return p.ID == id })]
}

// failed returns the error set for the next call of method, once.
func (s *memStore) failed(method string) error {
	err := s.fail[method]
	delete(s.fail, method)
	return err
}

func (s *memStore) InsertEvents(_ context.Context, evs ...audit.Event) error {
	s.events = append(s.events, evs...)
	return nil
}

func (s *memStore) WatchSet(_ context.Context, now, lateCutoff time.Time, limit int) ([]order.Order, error) {
	if err := s.failed("WatchSet"); err != nil {
		return nil, err
	}
	var due []*polled
	for _, p := range s.orders {
		watched := p.Status == order.Pending ||
			(p.Status == order.Underpaid || p.Status == order.Expired) && p.ExpiresAt.After(lateCutoff)
		if watched && p.next != nil && !p.next.After(now) {
			due = append(due, p)
		}
	}
	slices.SortStableFunc(due, func(a, b *polled) int { return a.next.Compare(*b.next) })
	out := []order.Order{}
	for _, p := range due[:min(limit, len(due))] {
		out = append(out, p.Order)
	}
	return out, nil
}

func (s *memStore) MarkPolled(_ context.Context, id uuid.UUID, next *time.Time) error {
	if err := s.failed("MarkPolled"); err != nil {
		return err
	}
	s.find(id).next = next
	return nil
}

func (s *memStore) KnownSignatures(_ context.Context, sigs []string) (map[string]bool, error) {
	if err := s.failed("KnownSignatures"); err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, sig := range sigs {
		if _, ok := s.ledger[sig]; ok {
			known[sig] = true
		}
	}
	return known, nil
}

func (s *memStore) Apply(_ context.Context, id uuid.UUID, decide func(order.Order) (payment.Decision, error)) (bool, error) {
	if err := s.failed("Apply"); err != nil {
		return false, err
	}
	p := s.find(id)
	d, err := decide(p.Order)
	if err != nil {
		return false, err
	}
	if _, dup := s.ledger[d.Payment.Signature]; dup {
		s.events = append(s.events, audit.Event{
			OrderID: &p.ID, Kind: audit.PaymentDuplicateIgnored, Message: "transaction already in the ledger", Signature: d.Payment.Signature,
		})
		return false, nil
	}
	if d.Status != p.Status {
		if err := order.Transition(p.Status, d.Status); err != nil {
			return false, err
		}
	}
	s.ledger[d.Payment.Signature] = d.Payment
	p.Status, p.ReceivedLamports, p.PaidAt = d.Status, d.Received, d.PaidAt
	s.events = append(s.events, d.Events...)
	return true, nil
}

// ofKind returns the events of kind k, in the order they were stored.
func (s *memStore) ofKind(k audit.Kind) []audit.Event {
	out := []audit.Event{}
	for _, ev := range s.events {
		if ev.Kind == k {
			out = append(out, ev)
		}
	}
	return out
}

// kinds returns the kinds of the events about the order with id, in the order they were stored.
func (s *memStore) kinds(id uuid.UUID) []audit.Kind {
	out := []audit.Kind{}
	for _, ev := range s.events {
		if ev.OrderID != nil && *ev.OrderID == id {
			out = append(out, ev.Kind)
		}
	}
	return out
}
