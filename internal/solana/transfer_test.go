package solana_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	sol "github.com/gagliardetto/solana-go"
	"github.com/mr-tron/base58"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/payment"
	"github.com/aleksiuno/solpay-shop/internal/solana"
	"github.com/aleksiuno/solpay-shop/internal/solana/solanatest"
)

// transfer is a payment of 0.001 SOL with two references.
func transfer() solana.Transfer {
	return solana.Transfer{
		Payer:      solanatest.Key("payer"),
		Recipient:  solanatest.Key("merchant"),
		Lamports:   1_000_000,
		References: []string{solanatest.Key("ref-2"), solanatest.Key("ref-1")},
		Blockhash:  solanatest.Key("blockhash"),
	}
}

// build returns the transaction for tr as solana-go decodes it, which is how a wallet reads it.
func build(t *testing.T, tr solana.Transfer) *sol.Transaction {
	t.Helper()
	b, err := solana.TransferTransaction(tr)
	require.NoError(t, err)
	tx, err := sol.TransactionFromBytes(b)
	require.NoError(t, err)
	return tx
}

// key returns solanatest.Key(label) as a solana-go key.
func key(label string) sol.PublicKey { return sol.MustPublicKeyFromBase58(solanatest.Key(label)) }

func TestTransferTransactionIsOneTransferForThePayerToSign(t *testing.T) {
	tx := build(t, transfer())

	assert.Equal(t, []sol.Signature{{}}, tx.Signatures, "one zeroed signature, for the payer's wallet to fill")
	assert.False(t, tx.Message.IsVersioned(), "a legacy transaction")
	assert.Equal(t, sol.MessageHeader{NumRequiredSignatures: 1, NumReadonlyUnsignedAccounts: 3}, tx.Message.Header)
	assert.Equal(t, solanatest.Key("blockhash"), tx.Message.RecentBlockhash.String())

	accounts, err := tx.Message.AccountMetaList()
	require.NoError(t, err)
	require.Len(t, accounts, 5)
	assert.Equal(t, sol.AccountMetaSlice{
		{PublicKey: key("payer"), IsSigner: true, IsWritable: true},
		{PublicKey: key("merchant"), IsWritable: true},
	}, accounts[:2], "the payer, who pays the fee and alone signs, then the recipient")
	assert.ElementsMatch(t, sol.AccountMetaSlice{
		{PublicKey: sol.MustPublicKeyFromBase58(solana.SystemProgram)},
		{PublicKey: key("ref-2")},
		{PublicKey: key("ref-1")},
	}, accounts[2:], "the program and the references are read-only")

	require.Len(t, tx.Message.Instructions, 1)
	inst := tx.Message.Instructions[0]
	program, err := tx.Message.Program(inst.ProgramIDIndex)
	require.NoError(t, err)
	assert.Equal(t, solana.SystemProgram, program.String())
	assert.Equal(t, transferData(1_000_000), []byte(inst.Data), "System Program instruction 2 (Transfer) with the amount in lamports")
}

func TestTransferTransactionListsTheReferencesAfterThePayerAndRecipient(t *testing.T) {
	tx := build(t, transfer())
	require.Len(t, tx.Message.Instructions, 1)
	accounts, err := tx.Message.Instructions[0].ResolveInstructionAccounts(&tx.Message)
	require.NoError(t, err)
	assert.Equal(t, []*sol.AccountMeta{
		{PublicKey: key("payer"), IsSigner: true, IsWritable: true},
		{PublicKey: key("merchant"), IsWritable: true},
		{PublicKey: key("ref-2")}, // neither writable nor a signer, in the Transfer's order
		{PublicKey: key("ref-1")},
	}, accounts)
}

// recorded returns the Transfer of the Devpay fixture, a payment that devpay made on devnet, and
// the transaction devpay sent for it, as recorded.
func recorded(t *testing.T) (solana.Transfer, []byte) {
	t.Helper()
	var res struct {
		Transaction []string `json:"transaction"` // the encoded transaction, then "base64"
	}
	require.NoError(t, json.Unmarshal(solanatest.Devpay.JSON(t), &res))
	require.Len(t, res.Transaction, 2)
	b, err := base64.StdEncoding.DecodeString(res.Transaction[0])
	require.NoError(t, err)
	tx, err := sol.TransactionFromBytes(b)
	require.NoError(t, err)
	return solana.Transfer{
		Payer:      solanatest.Payer,
		Recipient:  solanatest.Merchant,
		Lamports:   solanatest.Devpay.Lamports,
		References: []string{solanatest.Devpay.Reference},
		Blockhash:  tx.Message.RecentBlockhash.String(),
	}, b
}

func TestTransferTransactionRebuildsTheRecordedPayment(t *testing.T) {
	tr, sent := recorded(t)
	require.Equal(t, byte(1), sent[0], "one signature")
	want := slices.Clone(sent)
	clear(want[1 : 1+64])

	b, err := solana.TransferTransaction(tr)
	require.NoError(t, err)
	assert.Equal(t, want, b, "the bytes devpay sent to devnet, with the signature zeroed")
}

// The watcher reads a payment as getTransaction returns it once it lands. Wrapped that way, in
// the recorded payment's result, the built transaction pays the order. Verify reads no signature,
// so the zeroed one stands in for the wallet's.
func TestTransferTransactionPaysTheOrderOnceItLands(t *testing.T) {
	tr, _ := recorded(t)
	tr.Blockhash = solanatest.Key("another blockhash") // a new transaction, not the recorded one
	b, err := solana.TransferTransaction(tr)
	require.NoError(t, err)

	tx, err := solana.DecodeTxJSON(fixtureJSON(t, solanatest.Devpay, func(m map[string]any) {
		m["transaction"] = []any{base64.StdEncoding.EncodeToString(b), "base64"}
	}))
	require.NoError(t, err)
	assert.Equal(t, solanatest.Devpay.Keys, tx.Keys, "the recorded balances belong to the same keys")
	assert.Equal(t, payment.Verdict{Lamports: solanatest.Devpay.Lamports},
		payment.Verify(tx, solanatest.Merchant, solanatest.Devpay.Reference))
}

// references returns n distinct references.
func references(n int) []string {
	refs := make([]string, n)
	for i := range refs {
		refs[i] = solanatest.Key(fmt.Sprintf("ref-%d", i))
	}
	return refs
}

func TestTransferTransactionHoldsThirtyReferences(t *testing.T) {
	tr := transfer()
	tr.References = references(30)
	b, err := solana.TransferTransaction(tr)
	require.NoError(t, err)
	assert.Len(t, b, 1205, "33 bytes for each reference, within the 1232 bytes a transaction may take")
}

func TestTransferTransactionRefuses(t *testing.T) {
	short := base58.Encode(make([]byte, 31))
	zero := base58.Encode(make([]byte, 32))
	tests := map[string]struct {
		edit func(tr *solana.Transfer)
		want string // after "transfer: "
	}{
		"a payer that is not base58":    {func(tr *solana.Transfer) { tr.Payer = "not base58!" }, `payer "not base58!": `},
		"a recipient of 31 bytes":       {func(tr *solana.Transfer) { tr.Recipient = short }, `recipient "` + short + `": `},
		"a reference of 31 bytes":       {func(tr *solana.Transfer) { tr.References[1] = short }, `reference "` + short + `": `},
		"no blockhash":                  {func(tr *solana.Transfer) { tr.Blockhash = "" }, `blockhash "": `},
		"the zero blockhash":            {func(tr *solana.Transfer) { tr.Blockhash = zero }, `blockhash "` + zero + `": the zero hash, which no block has`},
		"no amount":                     {func(tr *solana.Transfer) { tr.Lamports = 0 }, "0 lamports: the amount must be positive"},
		"a negative amount":             {func(tr *solana.Transfer) { tr.Lamports = -1 }, "-1 lamports: the amount must be positive"},
		"no reference":                  {func(tr *solana.Transfer) { tr.References = nil }, "no reference, so the shop could not find the payment"},
		"an empty list of references":   {func(tr *solana.Transfer) { tr.References = []string{} }, "no reference, so the shop could not find the payment"},
		"a payer that is the recipient": {func(tr *solana.Transfer) { tr.Recipient = tr.Payer }, "the payer is the recipient"},
		"31 references": {
			func(tr *solana.Transfer) { tr.References = references(31) },
			"31 references make a 1238-byte transaction, over the 1232 bytes a transaction may take",
		},
	}
	for name, tt := range tests {
		tr := transfer()
		tt.edit(&tr)
		b, err := solana.TransferTransaction(tr)
		require.ErrorContains(t, err, "transfer: "+tt.want, name)
		assert.Nil(t, b, name)
	}
}
