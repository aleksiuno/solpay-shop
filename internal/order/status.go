// Package order holds orders: their statuses and the moves between them, the rule for crediting a
// payment, and the service that creates, gets and lists orders. It has no storage or network code;
// the service reaches the store through the Repository interface.
package order

// Status is where an order is in its life. Transition lists the moves between statuses.
type Status string

// The statuses an order can have. The status CHECK on the orders table lists the same values.
const (
	Pending   Status = "pending"   // waiting for payment
	Underpaid Status = "underpaid" // paid on time, but less than the amount
	Paid      Status = "paid"      // paid the amount or more on time
	Fulfilled Status = "fulfilled" // paid and downloaded
	Expired   Status = "expired"   // nothing paid by the deadline
	Refunded  Status = "refunded"  // marked refunded by an admin; final
)
