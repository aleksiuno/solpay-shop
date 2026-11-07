package payment

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/solana"
)

// Transactions builds the transactions with which browser wallets pay orders: the transfer that a
// Solana Pay wallet makes from an order's Request, for the wallet's account to sign and send. It
// is safe for concurrent use.
type Transactions struct {
	merchant    string
	blockhashes *blockhashCache
}

// NewTransactions returns Transactions for payments to merchant, with blockhashes from node, whose
// failures go to log. now times the blockhashes: outside tests it is time.Now, since their age is
// real time, not the time the shop stores, and the cache waits on the standard timers.
func NewTransactions(node Blockhasher, merchant string, now func() time.Time, log *slog.Logger) *Transactions {
	return &Transactions{merchant: merchant, blockhashes: &blockhashCache{node: node, now: now, log: log}}
}

// Build returns the unsigned transaction, in wire format, with which payer pays what Request(o,
// merchant) asks for, and the request's label and message for the wallet to show, such as
// "solpay-shop (devnet): Go payments checklist". The transaction names a blockhash whose fetch
// began less than 10 s before; when the node gives none, the error is a *NoBlockhashError.
//
// Build is meant for an order that can be paid now, as Order.Payable says, and does not check.
// It refuses a payer that is not an address or is the merchant, but only once it has the
// blockhash, so the caller should refuse them first.
func (t *Transactions) Build(ctx context.Context, o order.Order, payer string) ([]byte, string, error) {
	req := Request(o, t.merchant)
	hash, err := t.blockhashes.get(ctx)
	if err != nil {
		return nil, "", err
	}
	tx, err := solana.TransferTransaction(solana.Transfer{
		Payer: payer, Recipient: req.Recipient, Lamports: req.Lamports, References: req.References, Blockhash: hash,
	})
	if err != nil {
		return nil, "", fmt.Errorf("payment: order %s: %w", o.ID, err)
	}
	return tx, req.Label + ": " + req.Message, nil
}
