package solana_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	sol "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/solana"
	"github.com/aleksiuno/solpay-shop/internal/solana/solanatest"
)

// transferData is a System Program Transfer instruction's data: index 2, then the lamports.
func transferData(lamports int64) []byte {
	return binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint32(nil, 2), uint64(lamports))
}

func TestDecodeFixtures(t *testing.T) {
	for _, f := range solanatest.Fixtures {
		t.Run(f.File, func(t *testing.T) {
			tx := f.Tx(t)

			// Every key inside the encoded transaction is a named constant.
			assert.Equal(t, f.Signature, tx.Signature)
			assert.Equal(t, f.Keys, tx.Keys)
			assert.Equal(t, f.Slot, tx.Slot)
			assert.Equal(t, f.Failed, tx.Failed)
			assert.Equal(t, f.Pre, tx.Pre)
			assert.Equal(t, f.Post, tx.Post)
			require.NotNil(t, tx.BlockTime)
			assert.Equal(t, time.UTC, tx.BlockTime.Location())

			require.Len(t, tx.Instructions, 1)
			inst := tx.Instructions[0]
			assert.Equal(t, solana.SystemProgram, tx.Keys[inst.ProgramIndex])
			accounts := make([]string, len(inst.Accounts))
			for i, a := range inst.Accounts {
				accounts[i] = tx.Keys[a]
			}
			assert.Equal(t, []string{solanatest.Payer, solanatest.Merchant, f.Reference}, accounts,
				"payer, recipient, then the reference")
			assert.Equal(t, transferData(f.Lamports), inst.Data)
		})
	}
}

// fixtureJSON returns a fixture's JSON after edit has changed it. Numbers stay exact.
func fixtureJSON(t *testing.T, f solanatest.Fixture, edit func(m map[string]any)) []byte {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(f.JSON(t)))
	d.UseNumber()
	var m map[string]any
	require.NoError(t, d.Decode(&m))
	edit(m)
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return b
}

// object and array return v as a JSON object or array, failing the test if it is not one.
func object(t *testing.T, v any) map[string]any {
	t.Helper()
	o, ok := v.(map[string]any)
	require.True(t, ok, "want a JSON object, got %T", v)
	return o
}

func array(t *testing.T, v any) []any {
	t.Helper()
	a, ok := v.([]any)
	require.True(t, ok, "want a JSON array, got %T", v)
	return a
}

func TestDecodeAppendsLoadedAddresses(t *testing.T) {
	writable, readonly := solanatest.Key("loaded writable"), solanatest.Key("loaded readonly")
	b := fixtureJSON(t, solanatest.Devpay, func(m map[string]any) {
		meta := object(t, m["meta"])
		meta["loadedAddresses"] = map[string]any{"writable": []string{writable}, "readonly": []string{readonly}}
		for _, k := range []string{"preBalances", "postBalances"} {
			meta[k] = append(array(t, meta[k]), json.Number("7"), json.Number("8"))
		}
	})
	tx, err := solana.DecodeTxJSON(b)
	require.NoError(t, err)
	assert.Equal(t, append(append([]string(nil), solanatest.Devpay.Keys...), writable, readonly), tx.Keys,
		"static keys, then loaded writable, then loaded read-only")
	assert.Equal(t, []int64{7, 8}, tx.Pre[len(tx.Pre)-2:])
}

// encodedTx returns a base64 transaction from payer to merchant whose instruction edit has
// changed.
func encodedTx(t *testing.T, edit func(ci *sol.CompiledInstruction)) []any {
	t.Helper()
	payer := sol.MustPublicKeyFromBase58(solanatest.Key("payer"))
	transfer := system.NewTransferInstruction(1, payer, sol.MustPublicKeyFromBase58(solanatest.Key("merchant"))).Build()
	tx, err := sol.NewTransaction([]sol.Instruction{transfer}, sol.Hash{}, sol.TransactionPayer(payer))
	require.NoError(t, err)
	tx.Signatures = []sol.Signature{{}}
	edit(&tx.Message.Instructions[0])
	b, err := tx.MarshalBinary()
	require.NoError(t, err)
	return []any{base64.StdEncoding.EncodeToString(b), "base64"}
}

func TestDecodeRejects(t *testing.T) {
	threeBalances := func(m map[string]any) {
		object(t, m["meta"])["preBalances"] = []any{json.Number("1"), json.Number("0"), json.Number("1")}
		object(t, m["meta"])["postBalances"] = []any{json.Number("1"), json.Number("0"), json.Number("1")}
	}
	tests := map[string]struct {
		edit func(m map[string]any)
		want string
	}{
		"no meta":        {func(m map[string]any) { delete(m, "meta") }, "no transaction or no meta"},
		"no transaction": {func(m map[string]any) { delete(m, "transaction") }, "no transaction or no meta"},
		"truncated transaction": {func(m map[string]any) {
			m["transaction"] = []any{"AQID", "base64"}
		}, "decode transaction"},
		"a balance missing": {func(m map[string]any) {
			pre := array(t, object(t, m["meta"])["preBalances"])
			object(t, m["meta"])["preBalances"] = pre[:len(pre)-1]
		}, "3 before and 4 after for 4 accounts"},
		"a balance overflows": {func(m map[string]any) {
			array(t, object(t, m["meta"])["postBalances"])[1] = json.Number("9223372036854775808")
		}, "9223372036854775808 overflows int64"},
		"the slot overflows": {func(m map[string]any) {
			m["slot"] = json.Number("18446744073709551615")
		}, "slot: 18446744073709551615 overflows int64"},
		"program index out of range": {func(m map[string]any) {
			threeBalances(m)
			m["transaction"] = encodedTx(t, func(ci *sol.CompiledInstruction) { ci.ProgramIDIndex = 3 })
		}, "program index 3 of 3 accounts"},
		"account index out of range": {func(m map[string]any) {
			threeBalances(m)
			m["transaction"] = encodedTx(t, func(ci *sol.CompiledInstruction) { ci.Accounts = append(ci.Accounts, 3) })
		}, "account index 3 of 3 accounts"},
	}
	for name, tt := range tests {
		_, err := solana.DecodeTxJSON(fixtureJSON(t, solanatest.Devpay, tt.edit))
		require.ErrorContains(t, err, tt.want, name)
	}

	for _, raw := range []string{"null", "[]", `{"slot":"x"}`} {
		_, err := solana.DecodeTxJSON([]byte(raw))
		require.Error(t, err, raw)
	}
}
