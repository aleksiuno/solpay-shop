package solana

import (
	"errors"
	"fmt"

	sol "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/system"
)

// maxTransactionSize is the most bytes a transaction may take in wire format: it has to fit in
// one network packet.
const maxTransactionSize = 1232

// Transfer is a payment in SOL, as a Solana Pay wallet makes one from a transfer request. Every
// address and the blockhash are base58.
type Transfer struct {
	Payer     string // pays the amount and the fee, and is the only signer
	Recipient string // receives the amount
	Lamports  int64  // the amount, which must be positive
	// References are listed, in this order, as read-only accounts of the transfer, so the payment
	// can be found by querying any of them.
	References []string
	Blockhash  string // a recent blockhash, such as LatestBlockhash returns
}

// TransferTransaction returns t as an unsigned legacy transaction in wire format: one zeroed
// signature slot, which the payer's wallet fills, then the message. The message holds a single
// System Program transfer from the payer, who also pays the fee, to the recipient, with each
// reference appended to the transfer's accounts as neither writable nor a signer; the System
// Program ignores the extra accounts. That is the transaction a Solana Pay wallet builds from a
// transfer request, and the one payment.Verify accepts once it lands.
//
// TransferTransaction refuses an address or a blockhash that is not 32 bytes of base58, the zero
// blockhash, an amount that is not positive, a transfer with no reference, which the shop could
// not find, a payer that is the recipient, and a transaction over the 1232 bytes a transaction may
// take, which more than 30 references make.
func TransferTransaction(t Transfer) ([]byte, error) {
	payer, err := transferKey("payer", t.Payer)
	if err != nil {
		return nil, err
	}
	recipient, err := transferKey("recipient", t.Recipient)
	if err != nil {
		return nil, err
	}
	blockhash, err := transferKey("blockhash", t.Blockhash)
	if err != nil {
		return nil, err
	}
	switch {
	case blockhash.IsZero(): // what a reply without a blockhash decodes to
		return nil, fmt.Errorf("transfer: blockhash %q: the zero hash, which no block has", t.Blockhash)
	case t.Lamports <= 0:
		return nil, fmt.Errorf("transfer: %d lamports: the amount must be positive", t.Lamports)
	case len(t.References) == 0:
		return nil, errors.New("transfer: no reference, so the shop could not find the payment")
	case payer.Equals(recipient):
		return nil, errors.New("transfer: the payer is the recipient")
	}

	inst := system.NewTransferInstruction(uint64(t.Lamports), payer, recipient)
	for _, ref := range t.References {
		k, err := transferKey("reference", ref)
		if err != nil {
			return nil, err
		}
		inst.Append(sol.Meta(k))
	}
	tx, err := sol.NewTransaction([]sol.Instruction{inst.Build()}, sol.Hash(blockhash), sol.TransactionPayer(payer))
	if err != nil {
		return nil, fmt.Errorf("transfer: %w", err)
	}
	b, err := tx.MarshalBinary() // writes a zeroed signature for each signer it lacks
	if err != nil {
		return nil, fmt.Errorf("transfer: %w", err)
	}
	if len(b) > maxTransactionSize {
		return nil, fmt.Errorf("transfer: %d references make a %d-byte transaction, over the %d bytes a transaction may take",
			len(t.References), len(b), maxTransactionSize)
	}
	return b, nil
}

// transferKey decodes the base58 value of a Transfer's field.
func transferKey(field, s string) (sol.PublicKey, error) {
	k, err := sol.PublicKeyFromBase58(s)
	if err != nil {
		return sol.PublicKey{}, fmt.Errorf("transfer: %s %q: %w", field, s, err)
	}
	return k, nil
}
