package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	sol "github.com/gagliardetto/solana-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteKeypair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "payer.json")
	pub, err := writeKeypair(path)
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	key, err := sol.PrivateKeyFromSolanaKeygenFile(path)
	require.NoError(t, err, "solana-keygen format")
	assert.Equal(t, pub, key.PublicKey())

	before, err := os.ReadFile(path)
	require.NoError(t, err)
	_, err = writeKeypair(path)
	require.ErrorIs(t, err, os.ErrExist)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "an existing key file is never overwritten")
}

func TestRunUsage(t *testing.T) {
	tests := map[string]struct {
		args []string
		code int
		want string
	}{
		"help":           {[]string{"-h"}, 0, "Usage:"},
		"no arguments":   {nil, 2, "want -new-keypair FILE"},
		"extra argument": {[]string{"-new-keypair", "k.json", "more"}, 2, "want -new-keypair FILE"},
		"unknown flag":   {[]string{"-amount", "1"}, 2, "flag provided but not defined"},
	}
	for name, tt := range tests {
		var stdout, stderr bytes.Buffer
		code := run(tt.args, &stdout, &stderr)
		assert.Equal(t, tt.code, code, name)
		assert.Contains(t, stderr.String(), tt.want, name)
		assert.Empty(t, stdout.String(), name)
	}
}

func TestRunNewKeypair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "merchant.json")
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, run([]string{"-new-keypair", path}, &stdout, &stderr), stderr.String())

	key, err := sol.PrivateKeyFromSolanaKeygenFile(path)
	require.NoError(t, err)
	assert.Equal(t, key.PublicKey().String()+"\n", stdout.String(), "stdout is the address alone")
	assert.Contains(t, stderr.String(), "readable only by you")
	assert.NotContains(t, stderr.String()+stdout.String(), key.String(), "the secret key is never printed")
}
