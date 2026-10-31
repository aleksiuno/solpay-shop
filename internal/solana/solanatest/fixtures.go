// Package solanatest is test support for code that reads Solana: devnet transactions recorded as
// fixtures, their keys as constants, and deterministic keys for transactions built in tests.
package solanatest

import (
	"embed"
	"testing"

	"github.com/aleksiuno/solpay-shop/internal/solana"
)

// The fixtures are transfers between two throwaway devnet wallets, made only for these fixtures
// and funded only from the public faucet, so they link to no other wallet.
const (
	Payer    = "DNA1oaSc73PXFjhj2Q7XA7xjEnV6HRv4vUjWQx49UZMj"
	Merchant = "4KTW6LRNaDiob5rBDv3eSDMqSKkZquT4N8wByhYJBuZT"
)

// Fixture is a recorded getTransaction result and what it is known to contain. Every account key
// inside the encoded transaction is listed in Keys, so a test can pin all of them.
type Fixture struct {
	File      string // in testdata/
	Signature string
	Slot      int64
	Reference string   // the payment reference attached to the transfer
	Lamports  int64    // the amount the transfer asks for
	Failed    bool     // the transaction failed on chain
	Keys      []string // every account key, in order
	Pre, Post []int64  // lamport balances, aligned with Keys
}

var (
	// Devpay is a legacy transaction paid by devpay: 0.001 SOL with one reference.
	Devpay = Fixture{
		File:      "transfer_legacy_devpay.json",
		Signature: "4PPPPw3AW9WVKSAfMQSXEXK1ywtQoVcv7bc87gKTEWK88pQ4ToJcqW4cgM5VCddRrye2tL1qKEh7goHeU8Aby6Gu",
		Slot:      517259492,
		Reference: "DEYJ6sj9TS4DzVoYqxfBnLwjtf79zys7668uxPkrHSrp",
		Lamports:  1_000_000,
		Keys:      []string{Payer, Merchant, solana.SystemProgram, "DEYJ6sj9TS4DzVoYqxfBnLwjtf79zys7668uxPkrHSrp"},
		Pre:       []int64{5_000_000_000, 0, 1, 0},
		Post:      []int64{4_998_995_000, 1_000_000, 1, 0},
	}

	// V1 is the same kind of payment as a v1 transaction, the format whose compute budget sits
	// in the message.
	V1 = Fixture{
		File:      "transfer_v1.json",
		Signature: "3NF7FWCaycapgqW4oGFSTmXoekiSai1Qge7bHNonVqf7FpJcedryg6sLyjDXfaS1yeNPZsVcXkeZfQxidt93ZhM4",
		Slot:      517261300,
		Reference: "69ErW3Yge1LTurirNea8BpzuqVHGU4EmqpDPuaovtegM",
		Lamports:  1_000_000,
		Keys:      []string{Payer, Merchant, solana.SystemProgram, "69ErW3Yge1LTurirNea8BpzuqVHGU4EmqpDPuaovtegM"},
		Pre:       []int64{4_998_995_000, 1_000_000, 1, 0},
		Post:      []int64{4_997_990_000, 2_000_000, 1, 0},
	}

	// Failed asks for 10 SOL, more than the payer holds. It was sent without preflight, so it
	// landed and failed: the fee was charged and the merchant received nothing.
	Failed = Fixture{
		File:      "transfer_failed.json",
		Signature: "39eaMPK9CBfBB4mNKk6jYGBq8f2AB92c6YSVW5n1YgL7FuwuVRPARWHwwaEP2PLBfmqb7QEvGcBM2h5fF1DJQYj3",
		Slot:      517261348,
		Reference: "DCEwuPQ4vfbSSJQdvNPKSyx154mMmwx6gDjUguzbSzEm",
		Lamports:  10_000_000_000,
		Failed:    true,
		Keys:      []string{Payer, Merchant, solana.SystemProgram, "DCEwuPQ4vfbSSJQdvNPKSyx154mMmwx6gDjUguzbSzEm"},
		Pre:       []int64{4_997_990_000, 2_000_000, 1, 0},
		Post:      []int64{4_997_985_000, 2_000_000, 1, 0},
	}

	// Fixtures lists every recorded fixture.
	Fixtures = []Fixture{Devpay, V1, Failed}
)

//go:embed testdata/*.json
var testdata embed.FS

// JSON returns the recorded getTransaction result: the JSON value of its "result" field.
func (f Fixture) JSON(t testing.TB) []byte {
	t.Helper()
	b, err := testdata.ReadFile("testdata/" + f.File)
	if err != nil {
		t.Fatalf("read fixture %s: %v", f.File, err)
	}
	return b
}

// Tx returns the fixture decoded with solana.DecodeTxJSON.
func (f Fixture) Tx(t testing.TB) solana.Tx {
	t.Helper()
	tx, err := solana.DecodeTxJSON(f.JSON(t))
	if err != nil {
		t.Fatalf("decode fixture %s: %v", f.File, err)
	}
	return tx
}
