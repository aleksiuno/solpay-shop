package audit

// Level is how much attention an event needs. The database stores it next to the kind, so the
// log can be filtered by level.
type Level string

// The levels, as the database stores them.
const (
	Info  Level = "info"
	Warn  Level = "warn"
	Error Level = "error"
)

// Kind says what happened. Each kind has a fixed level.
type Kind string

// The kinds of event, grouped by the part of the shop they come from.
const (
	OrderCreated  Kind = "order.created"  // an order was placed
	OrderExpired  Kind = "order.expired"  // a pending order passed its deadline unpaid
	OrderRefunded Kind = "order.refunded" // an admin marked the order refunded

	PaymentSeen             Kind = "payment.seen"              // a transaction for an order, new to the ledger
	PaymentVerified         Kind = "payment.verified"          // a credit made the order paid
	PaymentDuplicateIgnored Kind = "payment.duplicate_ignored" // a transaction already in the ledger
	PaymentUnderpaid        Kind = "payment.underpaid"         // a credit left the order short
	PaymentOverpaid         Kind = "payment.overpaid"          // the order received more than its amount
	PaymentLate             Kind = "payment.late"              // a valid payment that does not count
	PaymentInvalidTx        Kind = "payment.invalid_tx"        // a transaction rejected as a payment

	DownloadIssued Kind = "download.issued" // the first authorised download fulfilled the order

	RPCError       Kind = "rpc.error"       // a failed RPC call that may work later
	RPCTimeout     Kind = "rpc.timeout"     // an RPC call that took too long
	WatcherStarted Kind = "watcher.started" // the payment watcher started polling
	WatcherStopped Kind = "watcher.stopped" // the payment watcher stopped
	WatcherError   Kind = "watcher.error"   // a watcher failure that is not the RPC node's
)

// Level returns the level of events of kind k. An unknown kind is a bug, so it gets Error.
func (k Kind) Level() Level {
	switch k {
	case OrderCreated, OrderExpired, OrderRefunded,
		PaymentSeen, PaymentVerified, PaymentDuplicateIgnored,
		DownloadIssued, WatcherStarted, WatcherStopped:
		return Info
	case PaymentUnderpaid, PaymentOverpaid, PaymentLate, PaymentInvalidTx, RPCTimeout:
		return Warn
	case RPCError, WatcherError:
		return Error
	}
	return Error
}
