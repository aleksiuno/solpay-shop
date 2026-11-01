// Package audit holds the payment event log's model: what an event is, its kinds and their levels.
// It imports no other package of the shop, so every package that emits events can import it
// without importing the others.
package audit

import (
	"time"
	"uuid"
)

// Event is one entry in the payment event log: something that happened to an order, or something
// the payment watcher saw. The log is append-only, and nothing reads it to decide an order's state.
type Event struct {
	ID        int64      // set by the database
	OrderID   *uuid.UUID // nil for an event that concerns no order, such as an RPC error
	Kind      Kind
	Message   string // one line for people
	Signature string // the transaction signature the event is about, or ""
	// Data holds the details for the kind, stored and logged as JSON, with keys in snake_case. Its
	// values must be plain JSON values: strings, numbers, booleans, and slices and maps of them.
	// JSON turns an error into {} and a time.Duration into nanoseconds, so pass err.Error() and
	// d.String() instead.
	Data      map[string]any
	CreatedAt time.Time // set by the database
}
