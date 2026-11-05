package solanatest

import (
	"context"
	"fmt"
	"sync"

	"github.com/aleksiuno/solpay-shop/internal/solana"
)

// The methods of a Fake, named after the JSON-RPC methods they stand for. FailNext and Calls take
// them.
const (
	GetSignaturesForAddress = "getSignaturesForAddress" // Signatures
	GetTransaction          = "getTransaction"          // Transaction
)

// Fake is a scripted RPC node that answers Signatures and Transaction as solana.Client does. It
// knows only the transactions it is given with Add. It is safe for concurrent use.
type Fake struct {
	mu    sync.Mutex
	sigs  map[string][]solana.SignatureInfo // by reference, oldest first
	txs   map[string]solana.Tx              // by signature
	fail  map[string]error                  // by method: the error of its next call
	calls map[string]int                    // by method
}

// NewFake returns a Fake that knows no transactions.
func NewFake() *Fake {
	return &Fake{
		sigs:  map[string][]solana.SignatureInfo{},
		txs:   map[string]solana.Tx{},
		fail:  map[string]error{},
		calls: map[string]int{},
	}
}

// Add lists tx as the newest transaction that mentions reference, and serves it from Transaction.
func (f *Fake) Add(reference string, tx solana.Tx) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sigs[reference] = append(f.sigs[reference], solana.SignatureInfo{
		Signature: tx.Signature, Slot: tx.Slot, BlockTime: tx.BlockTime, Failed: tx.Failed,
	})
	f.txs[tx.Signature] = tx
}

// FailNext makes the next call of method return err.
func (f *Fake) FailNext(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[method] = err
}

// Calls returns how many times method has been called.
func (f *Fake) Calls(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

// Signatures lists up to limit of the transactions added for reference, newest first.
func (f *Fake) Signatures(_ context.Context, reference string, limit int) ([]solana.SignatureInfo, error) {
	if err := f.begin(GetSignaturesForAddress); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.sigs[reference]
	out := []solana.SignatureInfo{}
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, all[i])
	}
	return out, nil
}

// Transaction returns the transaction added with signature. It gives solana.ErrNotFound for one
// that was never added, as a node does for one it does not have.
func (f *Fake) Transaction(_ context.Context, signature string) (solana.Tx, error) {
	if err := f.begin(GetTransaction); err != nil {
		return solana.Tx{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	tx, ok := f.txs[signature]
	if !ok {
		return solana.Tx{}, fmt.Errorf("%w: transaction %s", solana.ErrNotFound, signature)
	}
	return tx, nil
}

// begin counts a call of method and returns the error given for it, if any.
func (f *Fake) begin(method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[method]++
	err := f.fail[method]
	delete(f.fail, method)
	return err
}
