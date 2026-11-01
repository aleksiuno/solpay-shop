package order

import (
	"errors"
	"fmt"
)

// ErrInvalidTransition is returned for a status change that the state machine does not allow.
var ErrInvalidTransition = errors.New("order: invalid status transition")

// Transition returns nil if an order may move from one status to the other, and an error wrapping
// ErrInvalidTransition if not. Nine moves are allowed:
//
//	pending   → paid, underpaid, expired
//	underpaid → paid, refunded
//	paid      → fulfilled, refunded
//	fulfilled → refunded
//	expired   → refunded
//
// Every other pair is refused, including a status to itself and any value that is not a Status
// constant. Every status change must pass Transition first.
func Transition(from, to Status) error {
	var ok bool
	switch from {
	case Pending:
		ok = to == Paid || to == Underpaid || to == Expired
	case Underpaid:
		ok = to == Paid || to == Refunded
	case Paid:
		ok = to == Fulfilled || to == Refunded
	case Fulfilled, Expired:
		ok = to == Refunded
	case Refunded:
		// A refund is final.
	}
	if !ok {
		return fmt.Errorf("%w from %q to %q", ErrInvalidTransition, from, to)
	}
	return nil
}
