package order

import (
	"fmt"
	"math"
	"time"
	"uuid"
)

// Order is one purchase of a product, paid by one or more transfers that list its reference.
type Order struct {
	ID        uuid.UUID
	ProductID uuid.UUID
	Quantity  int
	// AmountLamports is the price times the quantity when the order was made. A later price
	// change does not alter it.
	AmountLamports int64
	// ReceivedLamports is the sum of the payments credited to the order. It can exceed the amount.
	ReceivedLamports int64
	// Reference is the address that the order's payments list as an extra account.
	Reference string
	Status    Status
	// ExpiresAt is the payment deadline. See OnTime.
	ExpiresAt   time.Time
	CreatedAt   time.Time
	PaidAt      *time.Time
	FulfilledAt *time.Time
	RefundedAt  *time.Time
}

// RemainingLamports returns how much is still to be paid: the amount less what was received, or
// 0 once the order has received the amount or more.
func (o Order) RemainingLamports() int64 {
	return max(o.AmountLamports-o.ReceivedLamports, 0)
}

// OnTime reports whether a payment made at blockTime counts towards the amount, which it does
// unless it is later than the deadline.
func (o Order) OnTime(blockTime time.Time) bool {
	return !blockTime.After(o.ExpiresAt)
}

// Credit works out what an on-time payment of lamports does to a pending or underpaid order: the
// status it moves to, and its new received total. The order is paid once the total reaches the
// amount, and underpaid while the total is short. Credit changes nothing itself.
//
// Crediting an order in any other status returns an error wrapping ErrInvalidTransition. A payment
// that is not positive, or one that would take the total above the int64 range, is also an error.
func (o Order) Credit(lamports int64) (next Status, received int64, err error) {
	if o.Status != Pending && o.Status != Underpaid {
		return "", 0, fmt.Errorf("%w: cannot credit a payment to an order that is %q", ErrInvalidTransition, o.Status)
	}
	if lamports <= 0 {
		return "", 0, fmt.Errorf("order: cannot credit %d lamports", lamports)
	}
	if lamports > math.MaxInt64-o.ReceivedLamports {
		return "", 0, fmt.Errorf("order: crediting %d lamports to %d received overflows int64", lamports, o.ReceivedLamports)
	}
	received = o.ReceivedLamports + lamports
	next = Underpaid
	if received >= o.AmountLamports {
		next = Paid
	}
	// A top-up that is still short leaves an underpaid order where it is: no move to check.
	if next != o.Status {
		if err := Transition(o.Status, next); err != nil {
			return "", 0, err
		}
	}
	return next, received, nil
}
