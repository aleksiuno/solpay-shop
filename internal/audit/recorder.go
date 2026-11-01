package audit

import (
	"context"
	"log/slog"
)

// Store saves events. Record calls it once with all the events it is given.
type Store interface {
	InsertEvents(ctx context.Context, evs ...Event) error
}

// Recorder saves events and writes them to the log.
type Recorder struct {
	store Store
	log   *slog.Logger
}

// NewRecorder returns a Recorder that saves events to store and logs them to log.
func NewRecorder(store Store, log *slog.Logger) *Recorder {
	return &Recorder{store: store, log: log}
}

// Record saves evs with one call to the store, then logs them. It returns no error, so a database
// outage never stops the caller: if the store fails, Record logs the failure at the error level and
// still logs every event.
func (r *Recorder) Record(ctx context.Context, evs ...Event) {
	if len(evs) == 0 {
		return
	}
	if err := r.store.InsertEvents(ctx, evs...); err != nil {
		r.log.LogAttrs(ctx, slog.LevelError, "events not stored",
			slog.Int("events", len(evs)), slog.Any("error", err))
	}
	r.Log(ctx, evs...)
}

// Log writes evs to the log without saving them. Code that saves events inside its own database
// transaction should call Log only after the commit, so a change that rolled back is never logged.
//
// Each event is one line at its kind's level, with the event's message and the attributes kind,
// component and, when set, order_id, signature and data. The level is the line's own, never an
// attribute, so a JSON line has a single level key.
func (r *Recorder) Log(ctx context.Context, evs ...Event) {
	for _, ev := range evs {
		attrs := []slog.Attr{
			slog.String("kind", string(ev.Kind)),
			slog.String("component", ev.Kind.component()),
		}
		if ev.OrderID != nil {
			attrs = append(attrs, slog.String("order_id", ev.OrderID.String()))
		}
		if ev.Signature != "" {
			attrs = append(attrs, slog.String("signature", ev.Signature))
		}
		if len(ev.Data) > 0 {
			attrs = append(attrs, slog.Any("data", ev.Data))
		}
		r.log.LogAttrs(ctx, ev.Kind.Level().slogLevel(), ev.Message, attrs...)
	}
}

// component names the part of the shop that events of kind k come from, for log lines. The payment
// watcher's own kinds belong to payment.
func (k Kind) component() string {
	switch k {
	case OrderCreated, OrderExpired, OrderRefunded:
		return "order"
	case PaymentSeen, PaymentVerified, PaymentDuplicateIgnored, PaymentUnderpaid, PaymentOverpaid,
		PaymentLate, PaymentInvalidTx, RPCError, RPCTimeout, WatcherStarted, WatcherStopped,
		WatcherError:
		return "payment"
	case DownloadIssued:
		return "download"
	}
	return "unknown"
}

// slogLevel maps l to the level of its log lines. An unknown level gets slog.LevelError.
func (l Level) slogLevel() slog.Level {
	switch l {
	case Info:
		return slog.LevelInfo
	case Warn:
		return slog.LevelWarn
	case Error:
		return slog.LevelError
	}
	return slog.LevelError
}
