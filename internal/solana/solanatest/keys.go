package solanatest

import (
	"crypto/ed25519"
	"crypto/sha256"

	sol "github.com/gagliardetto/solana-go"
)

// Key returns a deterministic test address: the ed25519 public key for the seed sha256(label), in
// base58. Key("merchant") is the same on every run, and it is nobody's wallet.
func Key(label string) string {
	seed := sha256.Sum256([]byte(label))
	return sol.PrivateKey(ed25519.NewKeyFromSeed(seed[:])).PublicKey().String()
}
