// Package money converts between lamports and decimal SOL strings. It uses integer arithmetic
// only: a float64 cannot hold every lamport amount exactly.
package money

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// LamportsPerSOL is the number of lamports in one SOL.
const LamportsPerSOL = 1_000_000_000

// decimals is the number of fraction digits a SOL amount can have.
const decimals = 9

// FormatSOL formats an amount of lamports as decimal SOL, with no trailing zeros and never in
// scientific notation: 1500000 is "0.0015" and 2000000000 is "2". A negative amount gets a
// leading minus sign.
func FormatSOL(lamports int64) string {
	sign := ""
	whole, frac := lamports/LamportsPerSOL, lamports%LamportsPerSOL
	if lamports < 0 {
		// Go division truncates towards zero, so both parts are negated without overflow,
		// even for math.MinInt64.
		sign, whole, frac = "-", -whole, -frac
	}
	s := sign + strconv.FormatInt(whole, 10)
	if frac == 0 {
		return s
	}
	return s + "." + strings.TrimRight(fmt.Sprintf("%0*d", decimals, frac), "0")
}

// ParseSOL parses a decimal SOL amount, such as "0.0015", into lamports. It accepts digits with an
// optional fraction of 1 to 9 digits, as Solana Pay URLs carry it: no sign, exponent, spaces,
// digit separators or bare decimal point.
func ParseSOL(s string) (int64, error) {
	whole, frac, hasDot := strings.Cut(s, ".")
	if !isDigits(whole) || hasDot && !isDigits(frac) {
		return 0, fmt.Errorf("invalid SOL amount %q: want digits with an optional fraction, such as 0.0015", s)
	}
	if len(frac) > decimals {
		return 0, fmt.Errorf("invalid SOL amount %q: more than %d decimal places", s, decimals)
	}
	var f int64 // the fraction in lamports: its digits, padded to nine with zeros
	for i := range decimals {
		f *= 10
		if i < len(frac) {
			f += int64(frac[i] - '0')
		}
	}
	// whole holds only digits, so ParseInt fails only when it is out of range.
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w > (math.MaxInt64-f)/LamportsPerSOL {
		return 0, fmt.Errorf("invalid SOL amount %q: too large", s)
	}
	return w*LamportsPerSOL + f, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
