package solanapay

import (
	"crypto/sha256"
	"testing"

	"github.com/mr-tron/base58"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// key returns a stable test address: the base58 encoding of sha256(label). It is nobody's
// wallet.
func key(label string) string {
	h := sha256.Sum256([]byte(label))
	return base58.Encode(h[:])
}

func TestURL(t *testing.T) {
	r := Request{
		Recipient:  key("merchant"),
		Lamports:   1_500_000,
		References: []string{key("ref-b"), key("ref-a")},
		Label:      "solpay-shop (devnet)",
		Message:    "Order 7: 2 × notes & more+",
	}
	want := "solana:" + key("merchant") +
		"?amount=0.0015" +
		"&reference=" + key("ref-b") +
		"&reference=" + key("ref-a") +
		"&label=solpay-shop%20%28devnet%29" +
		"&message=Order%207%3A%202%20%C3%97%20notes%20%26%20more%2B"
	assert.Equal(t, want, r.URL())
}

func TestURLOmitsEmptyFields(t *testing.T) {
	assert.Equal(t, "solana:"+key("merchant"), Request{Recipient: key("merchant")}.URL())
}

func TestRoundTrip(t *testing.T) {
	tests := []Request{
		{Recipient: key("merchant"), Lamports: 1_000_000, References: []string{key("ref")}, Label: "solpay-shop (devnet)", Message: "Solana Pay field notes"},
		{Recipient: key("merchant"), Lamports: 9_223_372_036_854_775_807},
		{Recipient: key("merchant"), References: []string{key("3"), key("1"), key("2")}},
		{Recipient: key("merchant"), Lamports: 1, Label: "ąčęėįšųūž 100% ?&=#+/"},
		{Recipient: "http111111111111111111111111111111111111111", Lamports: 1}, // base58 of 32 bytes, not a link
	}
	for _, want := range tests {
		got, err := Parse(want.URL())
		require.NoError(t, err, want.URL())
		assert.Equal(t, want, got, want.URL())
	}
}

func TestParse(t *testing.T) {
	got, err := Parse("SOLANA:" + key("merchant") + "?label=a+b&amount=0.001&reference=" + key("ref") + "&unknown=x")
	require.NoError(t, err)
	assert.Equal(t, Request{
		Recipient:  key("merchant"),
		Lamports:   1_000_000,
		References: []string{key("ref")},
		Label:      "a b", // "+" is a space in a query, as in URLSearchParams
	}, got)
}

func TestParseRejects(t *testing.T) {
	m, ref := key("merchant"), key("ref")
	short := base58.Encode(make([]byte, 31))
	tests := map[string]string{
		"https://" + m:                                     `scheme "https": want solana`,
		"solana:":                                          "no recipient",
		"solana:?amount=1":                                 "no recipient",
		"solana://" + m:                                    "no recipient",
		"solana:https://shop.example/pay":                  "transaction requests",
		"solana:HTTPS://shop.example/pay":                  "transaction requests",
		"solana:https%3A%2F%2Fshop.example%2Fpay":          "transaction requests",
		"solana:" + m + "0OIl":                             "recipient: ",
		"solana:" + short:                                  "recipient: ",
		"solana:" + m + "%zz":                              "recipient: ",
		"solana:" + m + "?amount=1e3":                      "amount: ",
		"solana:" + m + "?amount=-1":                       "amount: ",
		"solana:" + m + "?amount=.5":                       "amount: ",
		"solana:" + m + "?amount=":                         "amount: ",
		"solana:" + m + "?amount=0.0000000001":             "amount: ",
		"solana:" + m + "?amount=1&amount=2":               "amount appears more than once",
		"solana:" + m + "?label=a&label=b":                 "label appears more than once",
		"solana:" + m + "?amount=1&spl-token=" + ref:       "SPL token transfers are not supported",
		"solana:" + m + "?amount=1&memo=order-1":           "memos are not supported",
		"solana:" + m + "?amount=1&reference=" + short:     "reference: ",
		"solana:" + m + "?amount=1&reference=" + ref + "0": "reference: ",
		"solana:" + m + "?amount=1&label=%zz":              "query: ",
		"solana:" + m + "?amount=1;label=x":                "query: ",
	}
	for in, want := range tests {
		_, err := Parse(in)
		require.ErrorContains(t, err, want, in)
	}
}
