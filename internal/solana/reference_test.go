package solana

import (
	"testing"

	"github.com/mr-tron/base58"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewReference(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		ref := NewReference()
		b, err := base58.Decode(ref)
		require.NoError(t, err, ref)
		require.Len(t, b, 32, ref)
		require.False(t, seen[ref], "duplicate reference %s", ref)
		seen[ref] = true
	}
}

func TestDevnetGenesisHash(t *testing.T) {
	// Recorded from getGenesisHash on https://api.devnet.solana.com. A change here changes which
	// cluster the shop accepts, so it must be deliberate.
	assert.Equal(t, "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG", DevnetGenesisHash)
	b, err := base58.Decode(DevnetGenesisHash)
	require.NoError(t, err)
	assert.Len(t, b, 32)
}
