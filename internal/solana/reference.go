// Package solana holds what the shop needs from Solana. It is the only server package that imports
// the Solana SDK; the rest of the code works with plain Go types.
package solana

import (
	"crypto/rand"

	sol "github.com/gagliardetto/solana-go"
)

// DevnetGenesisHash is the genesis hash of Solana devnet, as getGenesisHash returns it. Code that
// moves or watches payments must check the RPC node's hash against it first, so it never acts on
// mainnet by mistake.
const DevnetGenesisHash = "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG"

// NewReference returns a fresh payment reference: 32 random bytes in base58, the form of a Solana
// address. The payment lists it as a read-only account, so the shop finds the payment by querying
// the reference. It is not a keypair: an account that never signs needs no private key, and it
// need not exist on chain, so the shop holds no keys at all.
func NewReference() string {
	var b [32]byte
	_, _ = rand.Read(b[:]) // never returns an error since Go 1.24
	return sol.PublicKey(b).String()
}
