package payment

import (
	"fmt"
	"time"
	"uuid"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/order"
)

// Outcome is what the payment ledger records a transaction as, for its order.
type Outcome string

// The outcomes, as the ledger's outcome CHECK lists them.
const (
	Credited Outcome = "credited" // counts towards the order's amount
	Late     Outcome = "late"     // a valid payment that does not count: after the deadline, or to an order that takes no more
	Rejected Outcome = "rejected" // not a payment to the order; the Payment's Reason says why
)

// Candidate is a transaction that lists an order's reference, with Verify's verdict on it.
type Candidate struct {
	Signature string
	Slot      int64
	BlockTime *time.Time // nil when the RPC node does not know it
	Verdict   Verdict
	Err       string // what went wrong reading it, for an Unfetchable verdict that came from an error
}

// Payment is one row of the payment ledger: what one transaction did for its order.
type Payment struct {
	Signature string
	OrderID   uuid.UUID
	Outcome   Outcome
	// Lamports is what the transaction paid. For a rejected one it is what its transfers claimed
	// when the reason is BalanceMismatch, and 0 for any other reason.
	Lamports  int64
	Slot      int64
	BlockTime *time.Time
	Reason    RejectReason // set only when the outcome is Rejected
}

// Decision is what a transaction does to an order: its ledger row, and the order's status, received
// total and paid time once the row is stored, with the events that tell of it.
type Decision struct {
	Payment  Payment
	Status   order.Status
	Received int64
	PaidAt   *time.Time
	Events   []audit.Event
}

// Decide works out what the transaction c does to the order o, at the time now. It changes
// nothing, so a caller can store the ledger row and the events together, and skip both when the
// row is already there; a transaction seen twice is then applied once.
//
// A rejected transaction is recorded as such, whatever the order's status. A valid one counts if
// it is on time: its block time is not after the order's deadline, or, when the RPC node gives no
// block time, now is not. An on-time payment to a pending or underpaid order is credited as the
// order's Credit rule says, and the order becomes paid at the payment's time once it has received
// its amount. One to a paid or fulfilled order, which can arrive in the same poll as the payment
// that paid it, is credited too, by the same rule, as an overpayment. Any other valid payment is
// late: after the deadline, or to an expired or refunded order. It is recorded, but it changes
// nothing; for an expired order, the late row is what lets an admin refund it. A valid verdict
// that pays nothing is an error, since Verify never gives one.
func Decide(o order.Order, c Candidate, now time.Time) (Decision, error) {
	d := Decision{
		Payment: Payment{
			Signature: c.Signature,
			OrderID:   o.ID,
			Lamports:  c.Verdict.Lamports,
			Slot:      c.Slot,
			BlockTime: c.BlockTime,
		},
		Status:   o.Status,
		Received: o.ReceivedLamports,
		PaidAt:   o.PaidAt,
	}
	event := func(k audit.Kind, msg string, data map[string]any) {
		d.Events = append(d.Events, audit.Event{OrderID: &o.ID, Kind: k, Message: msg, Signature: c.Signature, Data: data})
	}
	event(audit.PaymentSeen, "payment seen", map[string]any{"slot": c.Slot, "block_time": timeData(c.BlockTime)})

	if c.Verdict.Reason != "" {
		d.Payment.Outcome, d.Payment.Reason = Rejected, c.Verdict.Reason
		data := map[string]any{"reason": string(c.Verdict.Reason)}
		if c.Err != "" {
			data["error"] = c.Err
		}
		event(audit.PaymentInvalidTx, "transaction is not a payment to the order", data)
		return d, nil
	}

	if c.Verdict.Lamports <= 0 {
		return Decision{}, fmt.Errorf("payment: a valid verdict must pay a positive amount, not %d", c.Verdict.Lamports)
	}
	at := now
	if c.BlockTime != nil {
		at = *c.BlockTime
	}
	onTime := o.OnTime(at)
	lamports := c.Verdict.Lamports
	late := func() (Decision, error) {
		d.Payment.Outcome = Late
		event(audit.PaymentLate, "payment does not count towards the order", map[string]any{
			"lamports":   lamports,
			"block_time": timeData(c.BlockTime),
			"expires_at": timeData(&o.ExpiresAt),
			"on_time":    onTime,
		})
		return d, nil
	}

	switch o.Status {
	case order.Pending, order.Underpaid:
		if !onTime {
			return late()
		}
		next, received, err := o.Credit(lamports)
		if err != nil {
			return Decision{}, err
		}
		d.Payment.Outcome, d.Status, d.Received = Credited, next, received
		if next == order.Underpaid {
			event(audit.PaymentUnderpaid, "payment leaves the order short", map[string]any{
				"lamports":           lamports,
				"received_lamports":  received,
				"remaining_lamports": o.AmountLamports - received,
			})
			return d, nil
		}
		d.PaidAt = &at
		event(audit.PaymentVerified, "order paid", map[string]any{
			"lamports":          lamports,
			"received_lamports": received,
			"amount_lamports":   o.AmountLamports,
		})
		if received > o.AmountLamports {
			overpaid(event, received-o.AmountLamports)
		}
		return d, nil

	case order.Paid, order.Fulfilled:
		if !onTime {
			return late()
		}
		_, received, err := o.Credit(lamports)
		if err != nil {
			return Decision{}, err
		}
		d.Payment.Outcome, d.Received = Credited, received
		overpaid(event, received-o.AmountLamports)
		return d, nil

	case order.Expired, order.Refunded:
		return late()
	}
	return Decision{}, fmt.Errorf("payment: order %s is in an unknown status %q", o.ID, o.Status)
}

func overpaid(event func(audit.Kind, string, map[string]any), surplus int64) {
	event(audit.PaymentOverpaid, "order received more than its amount", map[string]any{"surplus_lamports": surplus})
}

// timeData is t as event data: an RFC 3339 string in UTC, or nil.
func timeData(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}
