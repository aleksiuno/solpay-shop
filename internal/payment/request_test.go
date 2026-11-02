package payment_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
	"github.com/aleksiuno/solpay-shop/internal/solanapay"
)

func TestRequest(t *testing.T) {
	o := order.Order{
		ProductTitle:   "Go payments checklist",
		Quantity:       2,
		AmountLamports: 10_000_000,
		Reference:      reference,
		Status:         order.Pending,
	}
	r := payment.Request(o, merchant)

	assert.Equal(t, solanapay.Request{
		Recipient:  merchant,
		Lamports:   10_000_000,
		References: []string{reference},
		Label:      "solpay-shop (devnet)",
		Message:    "Go payments checklist (2 licences)",
	}, r)
	assert.Equal(t, "solana:"+merchant+"?amount=0.01&reference="+reference+
		"&label=solpay-shop%20%28devnet%29&message=Go%20payments%20checklist%20%282%20licences%29", r.URL())

	// The URL is one that a payer can parse back, as devpay does.
	parsed, err := solanapay.Parse(r.URL())
	require.NoError(t, err)
	assert.Equal(t, r, parsed)
}

func TestRequestAsksForWhatRemains(t *testing.T) {
	o := order.Order{
		ProductTitle:     "Go payments checklist",
		Quantity:         1,
		AmountLamports:   10_000_000,
		ReceivedLamports: 4_000_000,
		Reference:        reference,
		Status:           order.Underpaid,
	}
	r := payment.Request(o, merchant)

	assert.Equal(t, int64(6_000_000), r.Lamports)
	assert.Equal(t, "Go payments checklist", r.Message, "one licence goes unsaid")
}
