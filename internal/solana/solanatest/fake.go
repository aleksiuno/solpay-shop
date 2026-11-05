package solanatest

import (
	"context"
	"fmt"
	"sync"

	"github.com/aleksiuno/solpay-shop/internal/solana"
)

// The methods of a Fake, named after the JSON-RPC methods they stand for. FailNext,
// BlockUntilDone and Calls take them.
const (
	GetGenesisHash          = "getGenesisHash"          // CheckDevnet
	GetSignaturesForAddress = "getSignaturesForAddress" // Signatures
	GetTransaction          = "getTransaction"          // Transaction
)

// Fake is a scripted RPC node with the methods of solana.Client. It serves devnet, and it knows
// only the transactions it is given with Add. It is safe for concurrent use.
type Fake struct {
	mu       sync.Mutex
	sigs     map[string][]solana.SignatureInfo // by reference, oldest first
	txs      map[string]solana.Tx              // by signature
	notFound map[string]int                    // by signature: how many more calls give ErrNotFound; < 0 means all
	fail     map[string]error                  // by method: the error of its next call
	block    map[string]bool                   // methods whose calls wait for their context to end
	calls    map[string]int                    // by method
}

// NewFake returns a Fake that knows no transactions.
func NewFake() *Fake {
	return &Fake{
		sigs:     map[string][]solana.SignatureInfo{},
		txs:      map[string]solana.Tx{},
		notFound: map[string]int{},
		fail:     map[string]error{},
		block:    map[string]bool{},
		calls:    map[string]int{},
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

// NotFound makes the next times calls of Transaction for signature give solana.ErrNotFound, as a
// node does while it has not indexed the transaction yet. With times < 0, every call does.
func (f *Fake) NotFound(signature string, times int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notFound[signature] = times
}

// FailNext makes the next call of method return err.
func (f *Fake) FailNext(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[method] = err
}

// BlockUntilDone makes every later call of method wait until its context ends and return the
// context's error, as a call to a node that does not answer does. An error given to FailNext
// comes first.
func (f *Fake) BlockUntilDone(method string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.block[method] = true
}

// Calls returns how many times method has been called.
func (f *Fake) Calls(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

// CheckDevnet returns nil, unless FailNext or BlockUntilDone says otherwise.
func (f *Fake) CheckDevnet(ctx context.Context) error {
	return f.begin(ctx, GetGenesisHash)
}

// Signatures lists up to limit of the transactions added for reference, newest first.
func (f *Fake) Signatures(ctx context.Context, reference string, limit int) ([]solana.SignatureInfo, error) {
	if err := f.begin(ctx, GetSignaturesForAddress); err != nil {
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
// that was never added, as a node does for one it does not have, and while NotFound says so.
func (f *Fake) Transaction(ctx context.Context, signature string) (solana.Tx, error) {
	if err := f.begin(ctx, GetTransaction); err != nil {
		return solana.Tx{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	tx, ok := f.txs[signature]
	if n := f.notFound[signature]; !ok || n != 0 {
		if n > 0 {
			f.notFound[signature] = n - 1
		}
		return solana.Tx{}, fmt.Errorf("%w: transaction %s", solana.ErrNotFound, signature)
	}
	return tx, nil
}

// begin counts a call of method and returns the error given to FailNext for it, if any. Otherwise,
// if the method is blocked, it waits for ctx to end and returns ctx's error.
func (f *Fake) begin(ctx context.Context, method string) error {
	f.mu.Lock()
	f.calls[method]++
	err, failing := f.fail[method]
	delete(f.fail, method)
	block := f.block[method]
	f.mu.Unlock()
	if !failing && block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}
