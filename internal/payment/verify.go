// Package payment attributes Solana payments to orders.
package payment

import (
	"encoding/binary"
	"math"
	"slices"

	"github.com/aleksiuno/solpay-shop/internal/solana"
)

// RejectReason says why a transaction pays nothing towards an order. The payment ledger stores it
// with a rejected payment.
type RejectReason string

const (
	// TxFailed: the transaction failed on chain, so its transfers moved nothing.
	TxFailed RejectReason = "tx_failed"
	// NoMatchingTransfer: no top-level System Program transfer pays the merchant and lists the
	// order's reference, or the ones that do pay 0 lamports.
	NoMatchingTransfer RejectReason = "no_matching_transfer"
	// BalanceMismatch: the matching transfers claim more than the merchant's balance grew by.
	BalanceMismatch RejectReason = "balance_mismatch"
	// Unfetchable: the transaction could not be read, or was read as something that cannot be
	// right, such as an index past its accounts or an amount beyond int64.
	Unfetchable RejectReason = "unfetchable"
)

// Verdict is what Verify finds in a transaction. Reason is empty when the transaction pays
// Lamports towards the order. A BalanceMismatch keeps the lamports its transfers claimed; every
// other rejection has Lamports 0.
type Verdict struct {
	Lamports int64
	Reason   RejectReason
}

// The System Program's Transfer instruction: data of a little-endian uint32 2, then the lamports
// as a little-endian uint64; accounts of the payer, the recipient, then any extra ones, which is
// where a Solana Pay wallet adds the references. The System Program ignores data after those 12
// bytes, so Verify does too.
const (
	transferIndex   = 2
	transferDataLen = 12
)

// Verify works out what tx pays to merchant for the order whose reference is reference. The
// payment is the sum of the transaction's top-level System Program transfers to merchant that list
// reference among their extra accounts. Other instructions are ignored, wherever they are, and so
// are transfers made by another program (inner instructions, which a Tx does not carry).
//
// Verify then checks the sum against the merchant's balance, which must have grown by at least as
// much. A transaction that claims more, such as one whose payer is the merchant, is a
// BalanceMismatch.
func Verify(tx solana.Tx, merchant, reference string) Verdict {
	if tx.Failed {
		return Verdict{Reason: TxFailed}
	}
	if !consistent(tx) {
		return Verdict{Reason: Unfetchable}
	}
	var sum int64
	for _, inst := range tx.Instructions {
		amount, ok := transfer(tx.Keys, inst, merchant, reference)
		if !ok {
			continue
		}
		// No real transfer comes near these limits: they would mean the transaction was misread.
		if amount > math.MaxInt64 {
			return Verdict{Reason: Unfetchable}
		}
		lamports := int64(amount)
		if lamports > math.MaxInt64-sum {
			return Verdict{Reason: Unfetchable}
		}
		sum += lamports
	}
	if sum == 0 {
		return Verdict{Reason: NoMatchingTransfer}
	}
	m := slices.Index(tx.Keys, merchant) // a matching transfer names merchant, so m >= 0
	if sum > tx.Post[m]-tx.Pre[m] {
		return Verdict{Lamports: sum, Reason: BalanceMismatch}
	}
	return Verdict{Lamports: sum}
}

// consistent reports whether tx has a balance before and after for each key, and whether every
// index in its instructions names a key. The solana package maps every transaction it returns this
// way, so a failure means the mapping is broken, not that the payer did something wrong.
func consistent(tx solana.Tx) bool {
	n := len(tx.Keys)
	if len(tx.Pre) != n || len(tx.Post) != n {
		return false
	}
	inRange := func(i int) bool { return i >= 0 && i < n }
	for _, inst := range tx.Instructions {
		if !inRange(inst.ProgramIndex) || slices.ContainsFunc(inst.Accounts, func(a int) bool { return !inRange(a) }) {
			return false
		}
	}
	return true
}

// transfer returns the lamports that inst moves if it is a System Program transfer to merchant
// that lists reference among its extra accounts.
func transfer(keys []string, inst solana.Instruction, merchant, reference string) (uint64, bool) {
	if keys[inst.ProgramIndex] != solana.SystemProgram ||
		len(inst.Data) < transferDataLen || binary.LittleEndian.Uint32(inst.Data) != transferIndex ||
		len(inst.Accounts) < 3 || keys[inst.Accounts[1]] != merchant {
		return 0, false
	}
	if !slices.ContainsFunc(inst.Accounts[2:], func(a int) bool { return keys[a] == reference }) {
		return 0, false
	}
	return binary.LittleEndian.Uint64(inst.Data[4:]), true
}
