package payment

import (
	"fmt"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/solanapay"
)

// label names the shop in the customer's wallet. It says devnet because a Solana Pay URL names no
// cluster: a wallet set to mainnet would offer to pay with real SOL.
const label = "solpay-shop (devnet)"

// Request returns the Solana Pay request for what is still to be paid on o: its remaining amount,
// to merchant, with the order's reference attached. The message names the product, and the number
// of licences when there is more than one.
//
// It is meant for an order that can still be paid: pending or underpaid, and not past its
// deadline. Request checks neither, so the caller must. Such an order always has a positive
// remaining amount; for one that has received its amount, the request would name no amount, and
// the wallet would ask the customer for one.
func Request(o order.Order, merchant string) solanapay.Request {
	msg := o.ProductTitle
	if o.Quantity > 1 {
		msg = fmt.Sprintf("%s (%d licences)", o.ProductTitle, o.Quantity)
	}
	return solanapay.Request{
		Recipient:  merchant,
		Lamports:   o.RemainingLamports(),
		References: []string{o.Reference},
		Label:      label,
		Message:    msg,
	}
}
