package money

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatAndParseSOL(t *testing.T) {
	tests := []struct {
		lamports int64
		sol      string
	}{
		{0, "0"},
		{1, "0.000000001"},
		{10, "0.00000001"},
		{1_000_000, "0.001"},
		{1_500_000, "0.0015"},
		{999_999_999, "0.999999999"},
		{LamportsPerSOL, "1"},
		{1_000_000_001, "1.000000001"},
		{12_340_000_000, "12.34"},
		{math.MaxInt64, "9223372036.854775807"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.sol, FormatSOL(tt.lamports), "FormatSOL(%d)", tt.lamports)
		got, err := ParseSOL(tt.sol)
		require.NoError(t, err, tt.sol)
		assert.Equal(t, tt.lamports, got, "ParseSOL(%q)", tt.sol)
	}
}

func TestFormatSOLNegative(t *testing.T) {
	assert.Equal(t, "-0.0015", FormatSOL(-1_500_000))
	assert.Equal(t, "-2", FormatSOL(-2*LamportsPerSOL))
	assert.Equal(t, "-9223372036.854775808", FormatSOL(math.MinInt64))
}

func TestParseSOLAcceptsPaddedForms(t *testing.T) {
	tests := map[string]int64{
		"1.0":         LamportsPerSOL,
		"1.500000000": 1_500_000_000,
		"0.10":        100_000_000,
		"007":         7 * LamportsPerSOL,
	}
	for in, want := range tests {
		got, err := ParseSOL(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
}

func TestParseSOLRejects(t *testing.T) {
	tests := map[string]string{
		"":                      "want digits",
		"1e3":                   "want digits",
		"-1":                    "want digits",
		"+1":                    "want digits",
		"1.":                    "want digits",
		".5":                    "want digits",
		"1.2.3":                 "want digits",
		" 1":                    "want digits",
		"1 ":                    "want digits",
		"1,5":                   "want digits",
		"1_000":                 "want digits",
		"0x10":                  "want digits",
		"١":                     "want digits", // an Arabic-Indic digit is not an ASCII digit
		"0.0000000001":          "more than 9 decimal places",
		"1.0000000000":          "more than 9 decimal places",
		"9223372036.854775808":  "too large",
		"9223372037":            "too large",
		"99999999999999999999":  "too large",
		"18446744073709551616":  "too large",
		"9223372036854775807.0": "too large",
	}
	for in, want := range tests {
		_, err := ParseSOL(in)
		require.ErrorContains(t, err, want, "ParseSOL(%q)", in)
	}
}

func FuzzFormatThenParse(f *testing.F) {
	for _, n := range []int64{0, 1, 1_500_000, LamportsPerSOL, math.MaxInt64} {
		f.Add(n)
	}
	f.Fuzz(func(t *testing.T, lamports int64) {
		if lamports < 0 {
			t.Skip("ParseSOL takes no sign")
		}
		got, err := ParseSOL(FormatSOL(lamports))
		require.NoError(t, err)
		require.Equal(t, lamports, got)
	})
}

func FuzzParseThenFormat(f *testing.F) {
	for _, s := range []string{"0", "0.0015", "1.500", "9223372036.854775807", "1e3", "-1", "1."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		lamports, err := ParseSOL(s)
		if err != nil {
			return
		}
		require.GreaterOrEqual(t, lamports, int64(0))
		// FormatSOL gives the canonical form of the same amount.
		again, err := ParseSOL(FormatSOL(lamports))
		require.NoError(t, err)
		require.Equal(t, lamports, again)
	})
}
