// Package solanapay builds and parses Solana Pay transfer-request URLs for SOL payments:
// solana:<recipient>?amount=<SOL>&reference=<address>&label=<text>&message=<text>.
package solanapay

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/mr-tron/base58"

	"github.com/aleksiuno/solpay-shop/internal/money"
)

// Request is a Solana Pay transfer request for SOL.
type Request struct {
	Recipient string // base58 address that receives the payment
	Lamports  int64  // amount to pay; 0 when the URL names no amount, so the wallet asks for one
	// References are base58 32-byte values that the wallet adds, in this order, as read-only
	// accounts to the transfer, so the payment can be found by querying any of them.
	References []string
	Label      string // who is asking for the payment, shown by the wallet
	Message    string // what the payment is for, shown by the wallet
}

// URL encodes r as a solana: URL with its fields in the order of the Solana Pay specification.
// The amount is included only when Lamports is positive. Text is percent-encoded with %20 for a
// space, as the Solana Pay specification asks. URL does not check the addresses; Parse does.
func (r Request) URL() string {
	var b strings.Builder
	b.WriteString("solana:" + r.Recipient)
	sep := "?"
	add := func(key, value string) {
		b.WriteString(sep + key + "=" + escape(value))
		sep = "&"
	}
	if r.Lamports > 0 {
		add("amount", money.FormatSOL(r.Lamports))
	}
	for _, ref := range r.References {
		add("reference", ref)
	}
	if r.Label != "" {
		add("label", r.Label)
	}
	if r.Message != "" {
		add("message", r.Message)
	}
	return b.String()
}

// escape is url.QueryEscape with %20 for a space. QueryEscape writes a literal "+" as %2B, so
// every "+" in its output stands for a space.
func escape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// Parse decodes a Solana Pay transfer-request URL for SOL. It rejects anything a payer could not
// pay exactly as asked: SPL token transfers, memos (which need a memo instruction), transaction
// requests, a field that may appear once but appears twice, a malformed amount and an address
// that is not 32 bytes of base58.
func Parse(raw string) (Request, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Request{}, fmt.Errorf("not a valid URL: %w", err)
	}
	if u.Scheme != "solana" { // url.Parse lowercases the scheme
		return Request{}, fmt.Errorf("scheme %q: want solana", u.Scheme)
	}
	recipient, err := url.PathUnescape(u.Opaque)
	switch {
	case err != nil:
		return Request{}, fmt.Errorf("recipient: %w", err)
	case recipient == "":
		return Request{}, errors.New("no recipient: want solana:<address>?amount=…")
	case strings.Contains(recipient, ":"): // a link; base58 has no colon
		return Request{}, errors.New("transaction requests (solana:https://…) are not supported")
	}
	if err := checkAddress(recipient); err != nil {
		return Request{}, fmt.Errorf("recipient: %w", err)
	}

	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Request{}, fmt.Errorf("query: %w", err)
	}
	for _, key := range []string{"amount", "spl-token", "label", "message", "memo"} {
		if len(q[key]) > 1 {
			return Request{}, fmt.Errorf("%s appears more than once", key)
		}
	}
	if q.Has("spl-token") {
		return Request{}, errors.New("SPL token transfers are not supported, only SOL")
	}
	if q.Has("memo") {
		return Request{}, errors.New("memos are not supported")
	}

	r := Request{Recipient: recipient, Label: q.Get("label"), Message: q.Get("message")}
	if q.Has("amount") {
		if r.Lamports, err = money.ParseSOL(q.Get("amount")); err != nil {
			return Request{}, fmt.Errorf("amount: %w", err)
		}
	}
	for _, ref := range q["reference"] {
		if err := checkAddress(ref); err != nil {
			return Request{}, fmt.Errorf("reference: %w", err)
		}
		r.References = append(r.References, ref)
	}
	return r, nil
}

// checkAddress reports whether s is the base58 encoding of 32 bytes, the form of a Solana
// address.
func checkAddress(s string) error {
	b, err := base58.Decode(s)
	if err != nil || len(b) != 32 {
		return fmt.Errorf("%q is not a base58 address of 32 bytes", s)
	}
	return nil
}
