package payment_test

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
	"github.com/aleksiuno/solpay-shop/internal/solana"
	"github.com/aleksiuno/solpay-shop/internal/solana/solanatest"
)

var (
	merchant  = solanatest.Key("merchant")
	payer     = solanatest.Key("payer")
	reference = solanatest.Key("reference")
	stranger  = solanatest.Key("stranger") // another wallet
	program   = solanatest.Key("program")  // a program that is not the System Program
)

// The indexes of the keys in a transaction that pay builds.
const (
	payerIdx = iota
	merchantIdx
	systemIdx
	referenceIdx
	strangerIdx
	programIdx
	numKeys
)

const fee = 5000

// pay returns a confirmed transaction in which payer sends merchant a System Program transfer of
// each amount, each listing reference, with balances that agree.
func pay(amounts ...int64) solana.Tx {
	tx := solana.Tx{
		Signature: "sig",
		Slot:      1,
		Keys:      []string{payer, merchant, solana.SystemProgram, reference, stranger, program},
		Pre:       []int64{10_000_000_000, 0, 1, 0, 0, 1},
		Post:      []int64{10_000_000_000 - fee, 0, 1, 0, 0, 1},
	}
	for _, a := range amounts {
		tx.Instructions = append(tx.Instructions, transfer(merchantIdx, uint64(a), referenceIdx))
		tx.Post[payerIdx] -= a
		tx.Post[merchantIdx] += a
	}
	return tx
}

// edit returns pay(1_000_000) after change has altered it.
func edit(change func(tx *solana.Tx)) solana.Tx {
	tx := pay(1_000_000)
	change(&tx)
	return tx
}

// transfer returns a System Program transfer of lamports from the payer to the key at index to,
// with the keys at extra as further accounts.
func transfer(to int, lamports uint64, extra ...int) solana.Instruction {
	return solana.Instruction{
		ProgramIndex: systemIdx,
		Accounts:     append([]int{payerIdx, to}, extra...),
		Data:         binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint32(nil, 2), lamports),
	}
}

func valid(lamports int64) payment.Verdict { return payment.Verdict{Lamports: lamports} }

func rejected(r payment.RejectReason) payment.Verdict { return payment.Verdict{Reason: r} }

// The recorded payments verify for their own merchant and reference, and for no other.
func TestVerifyRecordedTransactions(t *testing.T) {
	for _, f := range []solanatest.Fixture{solanatest.Devpay, solanatest.V1} {
		t.Run(f.File, func(t *testing.T) {
			tx := f.Tx(t)
			assert.Equal(t, valid(f.Lamports), payment.Verify(tx, solanatest.Merchant, f.Reference))
			assert.Equal(t, rejected(payment.NoMatchingTransfer), payment.Verify(tx, solanatest.Merchant, reference),
				"another order's reference")
			assert.Equal(t, rejected(payment.NoMatchingTransfer), payment.Verify(tx, solanatest.Payer, f.Reference),
				"another recipient")
		})
	}
	t.Run(solanatest.Failed.File, func(t *testing.T) {
		v := payment.Verify(solanatest.Failed.Tx(t), solanatest.Merchant, solanatest.Failed.Reference)
		assert.Equal(t, rejected(payment.TxFailed), v)
	})
}

// Verify reports what was paid, whatever the order's amount; the order's Credit then decides what
// that payment does to it.
func TestVerifyPaysTheOrder(t *testing.T) {
	o := order.Order{AmountLamports: 1_000_000, Status: order.Pending}
	tests := []struct {
		name string
		paid int64
		want order.Status
	}{
		{"exact", 1_000_000, order.Paid},
		{"under", 400_000, order.Underpaid},
		{"over", 1_500_000, order.Paid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := payment.Verify(pay(tt.paid), merchant, reference)
			require.Equal(t, valid(tt.paid), v)
			next, received, err := o.Credit(v.Lamports)
			require.NoError(t, err)
			assert.Equal(t, tt.want, next)
			assert.Equal(t, tt.paid, received)
		})
	}
}

func TestVerify(t *testing.T) {
	memo := solana.Instruction{ProgramIndex: programIdx, Accounts: []int{referenceIdx}, Data: []byte("order")}
	tests := []struct {
		name string
		tx   solana.Tx
		want payment.Verdict
	}{
		{"one transfer", pay(1_000_000), valid(1_000_000)},
		{"two transfers are summed", pay(300_000, 700_000), valid(1_000_000)},
		{"other instructions around the transfer are ignored", edit(func(tx *solana.Tx) {
			budget := solana.Instruction{ProgramIndex: programIdx, Data: []byte{2, 0x40, 0x0d, 0x03, 0}}
			tx.Instructions = []solana.Instruction{budget, tx.Instructions[0], memo}
		}), valid(1_000_000)},
		{"a transfer with only a payer is ignored", edit(func(tx *solana.Tx) {
			tx.Instructions = append(tx.Instructions, solana.Instruction{
				ProgramIndex: systemIdx, Accounts: []int{payerIdx}, Data: tx.Instructions[0].Data,
			})
		}), valid(1_000_000)},
		{"a transfer without the reference is not counted", edit(func(tx *solana.Tx) {
			tx.Instructions = append(tx.Instructions, transfer(merchantIdx, 500_000))
			tx.Post[payerIdx] -= 500_000
			tx.Post[merchantIdx] += 500_000
		}), valid(1_000_000)},
		{"the merchant and reference among the loaded keys", solana.Tx{
			// Static keys, then a lookup table's writable and read-only ones.
			Keys: []string{payer, solana.SystemProgram, merchant, reference},
			Pre:  []int64{10_000_000_000, 1, 0, 0},
			Post: []int64{10_000_000_000 - fee - 1_000_000, 1, 1_000_000, 0},
			Instructions: []solana.Instruction{{
				ProgramIndex: 1,
				Accounts:     []int{0, 2, 3},
				Data:         transfer(0, 1_000_000).Data,
			}},
		}, valid(1_000_000)},

		{"a failed transaction", edit(func(tx *solana.Tx) { tx.Failed = true }), rejected(payment.TxFailed)},
		{"a failed transaction that was also misread", edit(func(tx *solana.Tx) {
			tx.Failed = true
			tx.Pre = tx.Pre[:1]
		}), rejected(payment.TxFailed)},
		{"no instructions", edit(func(tx *solana.Tx) { tx.Instructions = nil }), rejected(payment.NoMatchingTransfer)},
		{"to another recipient", edit(func(tx *solana.Tx) {
			tx.Instructions[0] = transfer(strangerIdx, 1_000_000, referenceIdx)
			tx.Post[merchantIdx] -= 1_000_000
			tx.Post[strangerIdx] += 1_000_000
		}), rejected(payment.NoMatchingTransfer)},
		{"no reference", edit(func(tx *solana.Tx) {
			tx.Instructions[0] = transfer(merchantIdx, 1_000_000)
		}), rejected(payment.NoMatchingTransfer)},
		{"another order's reference", edit(func(tx *solana.Tx) {
			tx.Instructions[0] = transfer(merchantIdx, 1_000_000, strangerIdx)
		}), rejected(payment.NoMatchingTransfer)},
		{"the reference only on another instruction", edit(func(tx *solana.Tx) {
			tx.Instructions = []solana.Instruction{transfer(merchantIdx, 1_000_000), memo}
		}), rejected(payment.NoMatchingTransfer)},
		{"a transfer's data for another program", edit(func(tx *solana.Tx) {
			tx.Instructions[0].ProgramIndex = programIdx
		}), rejected(payment.NoMatchingTransfer)},
		{"trailing data after a transfer's, which the System Program ignores", edit(func(tx *solana.Tx) {
			tx.Instructions[0].Data = append(tx.Instructions[0].Data, 0)
		}), valid(1_000_000)},
		{"data shorter than a transfer's", edit(func(tx *solana.Tx) {
			tx.Instructions[0].Data = tx.Instructions[0].Data[:11]
		}), rejected(payment.NoMatchingTransfer)},
		{"another System instruction that pays a uint64", edit(func(tx *solana.Tx) {
			tx.Instructions[0].Data[0] = 5 // WithdrawNonceAccount, which pays its second account
		}), rejected(payment.NoMatchingTransfer)},
		{"a transfer of nothing", edit(func(tx *solana.Tx) {
			tx.Instructions[0] = transfer(merchantIdx, 0, referenceIdx)
		}), rejected(payment.NoMatchingTransfer)},

		{"more claimed than the merchant received", edit(func(tx *solana.Tx) {
			tx.Post[merchantIdx] = 999_999
		}), payment.Verdict{Lamports: 1_000_000, Reason: payment.BalanceMismatch}},
		{"the merchant pays itself", solana.Tx{
			Keys: []string{merchant, solana.SystemProgram, reference},
			Pre:  []int64{10_000_000_000, 1, 0},
			Post: []int64{10_000_000_000 - fee, 1, 0},
			Instructions: []solana.Instruction{{
				ProgramIndex: 1,
				Accounts:     []int{0, 0, 2},
				Data:         transfer(0, 1_000_000).Data,
			}},
		}, payment.Verdict{Lamports: 1_000_000, Reason: payment.BalanceMismatch}},

		{"a balance missing before", edit(func(tx *solana.Tx) { tx.Pre = tx.Pre[:numKeys-1] }), rejected(payment.Unfetchable)},
		{"a balance missing after", edit(func(tx *solana.Tx) { tx.Post = tx.Post[:numKeys-1] }), rejected(payment.Unfetchable)},
		{"a program index past the keys", edit(func(tx *solana.Tx) {
			tx.Instructions = append(tx.Instructions, solana.Instruction{ProgramIndex: numKeys})
		}), rejected(payment.Unfetchable)},
		{"a negative program index", edit(func(tx *solana.Tx) {
			tx.Instructions = append(tx.Instructions, solana.Instruction{ProgramIndex: -1})
		}), rejected(payment.Unfetchable)},
		{"an account index past the keys", edit(func(tx *solana.Tx) {
			tx.Instructions[0].Accounts = append(tx.Instructions[0].Accounts, numKeys)
		}), rejected(payment.Unfetchable)},
		{"a negative account index", edit(func(tx *solana.Tx) {
			tx.Instructions[0].Accounts[0] = -1
		}), rejected(payment.Unfetchable)},
		{"an amount of exactly the int64 maximum", pay(math.MaxInt64), valid(math.MaxInt64)},
		{"an amount beyond int64", edit(func(tx *solana.Tx) {
			tx.Instructions[0] = transfer(merchantIdx, math.MaxInt64+1, referenceIdx)
		}), rejected(payment.Unfetchable)},
		{"a sum beyond int64", edit(func(tx *solana.Tx) {
			tx.Instructions = []solana.Instruction{
				transfer(merchantIdx, math.MaxInt64, referenceIdx),
				transfer(merchantIdx, 1, referenceIdx),
			}
		}), rejected(payment.Unfetchable)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, payment.Verify(tt.tx, merchant, reference))
		})
	}
}

// The reference counts only among a transfer's extra accounts, not as its payer or recipient.
func TestVerifyLooksForTheReferenceAmongTheExtraAccounts(t *testing.T) {
	tx := edit(func(tx *solana.Tx) { tx.Instructions[0] = transfer(merchantIdx, 1_000_000, strangerIdx) })
	assert.Equal(t, valid(1_000_000), payment.Verify(tx, merchant, stranger))
	assert.Equal(t, rejected(payment.NoMatchingTransfer), payment.Verify(tx, merchant, payer), "the payer")
	assert.Equal(t, rejected(payment.NoMatchingTransfer), payment.Verify(tx, merchant, merchant), "the recipient")
}
