package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sol "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/gagliardetto/solana-go/rpc/jsonrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/solana"
	"github.com/aleksiuno/solpay-shop/internal/solanapay"
)

// testKey returns a keypair derived from sha256(label), the same on every run. It is nobody's
// wallet.
func testKey(label string) sol.PrivateKey {
	seed := sha256.Sum256([]byte(label))
	return sol.PrivateKey(ed25519.NewKeyFromSeed(seed[:]))
}

func addr(label string) string { return testKey(label).PublicKey().String() }

// request parses the URL a shop would show, so the tests cover the whole path from URL to
// transaction.
func request(t *testing.T, r solanapay.Request) solanapay.Request {
	t.Helper()
	req, err := solanapay.Parse(r.URL())
	require.NoError(t, err)
	return req
}

// order is a typical request: 0.001 SOL to the merchant with one reference.
func order(t *testing.T) solanapay.Request {
	return request(t, solanapay.Request{Recipient: addr("merchant"), Lamports: 1_000_000, References: []string{addr("ref")}})
}

func explorer(sig sol.Signature) string {
	return "https://explorer.solana.com/tx/" + sig.String() + "?cluster=devnet"
}

func TestTransferAttachesReferences(t *testing.T) {
	payer := testKey("payer").PublicKey()
	req := request(t, solanapay.Request{
		Recipient:  addr("merchant"),
		Lamports:   1_000_000,
		References: []string{addr("ref-2"), addr("ref-1")},
	})
	inst, err := transfer(req, payer)
	require.NoError(t, err)

	assert.Equal(t, system.ProgramID, inst.ProgramID())
	data, err := inst.Data()
	require.NoError(t, err)
	assert.Equal(t, binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint32(nil, 2), 1_000_000), data,
		"System Program instruction 2 (Transfer) with the amount in lamports")

	want := []*sol.AccountMeta{
		{PublicKey: payer, IsSigner: true, IsWritable: true},
		{PublicKey: testKey("merchant").PublicKey(), IsWritable: true},
		{PublicKey: testKey("ref-2").PublicKey()}, // read-only and not a signer, in URL order
		{PublicKey: testKey("ref-1").PublicKey()},
	}
	assert.Equal(t, want, inst.Accounts())
}

func TestTransferRefuses(t *testing.T) {
	payer := testKey("payer").PublicKey()
	tests := map[string]struct {
		req  solanapay.Request
		want string
	}{
		"no reference":       {solanapay.Request{Recipient: addr("merchant"), Lamports: 1}, "shop could not detect"},
		"no amount":          {solanapay.Request{Recipient: addr("merchant"), References: []string{addr("ref")}}, "names no amount"},
		"payer is recipient": {solanapay.Request{Recipient: payer.String(), Lamports: 1, References: []string{addr("ref")}}, "payer is the recipient"},
	}
	for name, tt := range tests {
		_, err := transfer(request(t, tt.req), payer)
		require.ErrorContains(t, err, tt.want, name)
	}
}

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

// fakeRPC is a JSON-RPC endpoint that answers each method with its handler's result and records
// the methods called. Handlers run on the server's goroutines, so they report with assert.
type fakeRPC struct {
	t        *testing.T
	url      string
	handlers map[string]func(params []json.RawMessage) any

	mu    sync.Mutex
	calls []string
}

func newFakeRPC(t *testing.T, handlers map[string]func(params []json.RawMessage) any) (*fakeRPC, *rpc.Client) {
	f := &fakeRPC{t: t, handlers: handlers}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f, rpc.New(srv.URL)
}

func (f *fakeRPC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage   `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("decode the JSON-RPC request: %v", err)
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, req.Method)
	f.mu.Unlock()
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	h, ok := f.handlers[req.Method]
	if !ok {
		h = func([]json.RawMessage) any { return failure{Code: -32601, Message: "Method not found"} }
	}
	switch res := h(req.Params); res.(type) {
	case hangUp:
		hangUpOn(f.t, w)
		return
	case failure:
		resp["error"] = res
	default:
		resp["result"] = res
	}
	w.Header().Set("Content-Type", "application/json")
	assert.NoError(f.t, json.NewEncoder(w).Encode(resp))
}

// failure is a handler result that fakeRPC sends as a JSON-RPC error.
type failure struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// hangUp is a handler result that makes fakeRPC close the connection without an answer, as when
// the node takes a request and the response is lost in transit.
type hangUp struct{}

func hangUpOn(t *testing.T, w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Error("the response writer cannot be hijacked")
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)
		return
	}
	_ = conn.Close()
}

func (f *fakeRPC) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func withContext(value any) any {
	return map[string]any{"context": map[string]any{"slot": 1}, "value": value}
}

// sentTx holds the transaction the fake node received.
type sentTx struct {
	mu sync.Mutex
	tx *sol.Transaction
}

func (s *sentTx) get() *sol.Transaction {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tx
}

// devnet returns handlers for a devnet node where the payer holds balance lamports and every sent
// transaction is confirmed at once, and the transaction it receives.
func devnet(t *testing.T, balance uint64) (map[string]func([]json.RawMessage) any, *sentTx) {
	sent := &sentTx{}
	return map[string]func([]json.RawMessage) any{
		"getGenesisHash": func([]json.RawMessage) any { return solana.DevnetGenesisHash },
		"getBalance":     func([]json.RawMessage) any { return withContext(balance) },
		"getLatestBlockhash": func([]json.RawMessage) any {
			return withContext(map[string]any{"blockhash": addr("blockhash"), "lastValidBlockHeight": 1000})
		},
		"sendTransaction": func(params []json.RawMessage) any {
			var b64 string
			var opts struct {
				SkipPreflight       bool   `json:"skipPreflight"`
				PreflightCommitment string `json:"preflightCommitment"`
			}
			if len(params) != 2 || json.Unmarshal(params[0], &b64) != nil || json.Unmarshal(params[1], &opts) != nil {
				t.Errorf("sendTransaction params: %s", params)
				return failure{Code: -32602, Message: "Invalid params"}
			}
			assert.False(t, opts.SkipPreflight, "the node simulates the transaction before it sends it")
			assert.Equal(t, "confirmed", opts.PreflightCommitment)
			tx, err := sol.TransactionFromBase64(b64)
			if err != nil {
				t.Errorf("decode the sent transaction: %v", err)
				return failure{Code: -32602, Message: "Invalid params"}
			}
			sent.mu.Lock()
			sent.tx = tx
			sent.mu.Unlock()
			return tx.Signatures[0].String()
		},
		"getSignatureStatuses": func([]json.RawMessage) any {
			return withContext([]any{map[string]any{"slot": 5, "confirmations": 0, "err": nil, "confirmationStatus": "confirmed"}})
		},
	}, sent
}

func TestPay(t *testing.T) {
	key := testKey("payer")
	handlers, sent := devnet(t, 2_000_000)
	f, client := newFakeRPC(t, handlers)

	var log bytes.Buffer
	sig, err := pay(t.Context(), client, key, order(t), &log)
	require.NoError(t, err)

	assert.Equal(t, []string{"getGenesisHash", "getBalance", "getLatestBlockhash", "sendTransaction", "getSignatureStatuses"}, f.called())
	tx := sent.get()
	require.NotNil(t, tx)
	assert.Equal(t, tx.Signatures[0], sig)
	require.NoError(t, tx.VerifySignatures())
	assert.Equal(t, testKey("blockhash").PublicKey().String(), tx.Message.RecentBlockhash.String())

	require.Len(t, tx.Message.Instructions, 1)
	accounts, err := tx.Message.Instructions[0].ResolveInstructionAccounts(&tx.Message)
	require.NoError(t, err)
	require.Len(t, accounts, 3)
	assert.Equal(t, key.PublicKey(), accounts[0].PublicKey)
	assert.Equal(t, testKey("merchant").PublicKey(), accounts[1].PublicKey)
	assert.Equal(t, &sol.AccountMeta{PublicKey: testKey("ref").PublicKey()}, accounts[2],
		"the reference is a read-only, non-signer account of the transfer")

	out := log.String()
	assert.Contains(t, out, "Paying 0.001 SOL from "+key.PublicKey().String()+" to "+addr("merchant"))
	link := strings.Index(out, explorer(sig))
	require.GreaterOrEqual(t, link, 0)
	assert.Less(t, link, strings.Index(out, "Sent;"), "the transaction is shown before it is sent")
}

func TestPayShowsTheTransactionWhenTheSendIsLost(t *testing.T) {
	handlers, sent := devnet(t, 2_000_000)
	accept := handlers["sendTransaction"]
	handlers["sendTransaction"] = func(params []json.RawMessage) any {
		accept(params) // the node takes the transaction, but its answer never arrives
		return hangUp{}
	}
	_, client := newFakeRPC(t, handlers)

	var log bytes.Buffer
	_, err := pay(t.Context(), client, testKey("payer"), order(t), &log)
	require.ErrorContains(t, err, "the node may have received it")
	tx := sent.get()
	require.NotNil(t, tx)
	assert.Contains(t, log.String(), explorer(tx.Signatures[0]))
}

func TestPayRefusesOtherClusters(t *testing.T) {
	handlers, _ := devnet(t, 2_000_000)
	handlers["getGenesisHash"] = func([]json.RawMessage) any { return addr("another cluster") }
	f, client := newFakeRPC(t, handlers)

	_, err := pay(t.Context(), client, testKey("payer"), order(t), io.Discard)
	require.ErrorContains(t, err, "not on devnet")
	assert.Equal(t, []string{"getGenesisHash"}, f.called())
}

func TestPayRefusesTooSmallBalance(t *testing.T) {
	handlers, _ := devnet(t, 999_999)
	f, client := newFakeRPC(t, handlers)

	_, err := pay(t.Context(), client, testKey("payer"), order(t), io.Discard)
	require.ErrorContains(t, err, "holds 0.000999999 SOL, and the payment needs 0.001 SOL")
	require.ErrorContains(t, err, faucetURL)
	assert.Equal(t, []string{"getGenesisHash", "getBalance"}, f.called())
}

func TestPayShowsRPCErrorsOnOneLine(t *testing.T) {
	handlers, _ := devnet(t, 2_000_000)
	handlers["sendTransaction"] = func([]json.RawMessage) any {
		return failure{
			Code:    -32002,
			Message: "Transaction simulation failed: Error processing Instruction 0: custom program error: 0x1",
			Data: map[string]any{"err": map[string]any{"InstructionError": []any{0, map[string]any{"Custom": 1}}},
				"logs": []string{"Program 11111111111111111111111111111111 invoke [1]", "Transfer: insufficient lamports 0, need 1000000"}},
		}
	}
	_, client := newFakeRPC(t, handlers)

	_, err := pay(t.Context(), client, testKey("payer"), order(t), io.Discard)
	require.EqualError(t, err, "send the transaction: RPC error -32002: "+
		"Transaction simulation failed: Error processing Instruction 0: custom program error: 0x1\n"+
		"  Program 11111111111111111111111111111111 invoke [1]\n"+
		"  Transfer: insufficient lamports 0, need 1000000")
	_, ok := errors.AsType[*jsonrpc.RPCError](err)
	assert.True(t, ok, "the RPC error is still in the chain")
}

func TestPayChecksTheRequestFirst(t *testing.T) {
	handlers, _ := devnet(t, 2_000_000)
	f, client := newFakeRPC(t, handlers)
	req := request(t, solanapay.Request{Recipient: addr("merchant"), Lamports: 1_000_000})

	_, err := pay(t.Context(), client, testKey("payer"), req, io.Discard)
	require.ErrorContains(t, err, "no reference")
	assert.Empty(t, f.called(), "no RPC call for a request devpay refuses")
}

func TestConfirm(t *testing.T) {
	const lastValid = 100
	processed := map[string]any{"slot": 5, "err": nil, "confirmationStatus": "processed"}
	confirmed := map[string]any{"slot": 5, "err": nil, "confirmationStatus": "confirmed"}
	tests := map[string]struct {
		statuses    []any         // one per getSignatureStatuses call; the last one repeats
		heights     []uint64      // one per getBlockHeight call; the last one repeats
		timeout     time.Duration // 0 = 5s, which only a hang reaches
		want        string        // "" = confirmed
		statusCalls int           // 0 = not checked
	}{
		"confirmed": {
			statuses: []any{nil, processed, confirmed}, heights: []uint64{90}, statusCalls: 3,
		},
		"failed on chain": {
			statuses: []any{map[string]any{"slot": 5, "err": map[string]any{"InstructionError": []any{0, "Custom"}}, "confirmationStatus": "confirmed"}},
			heights:  []uint64{90}, want: "failed on chain", statusCalls: 1,
		},
		"expired": {
			statuses: []any{nil}, heights: []uint64{99, 101}, want: "expired before it landed", statusCalls: 3,
		},
		"landed while the height passed": {
			statuses: []any{nil, nil, confirmed}, heights: []uint64{99, 101}, statusCalls: 3,
		},
		"processed until the deadline": {
			statuses: []any{processed}, heights: []uint64{90}, timeout: 50 * time.Millisecond, want: "no confirmation yet",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			calls := map[string]int{}
			next := func(method string, n int) int {
				mu.Lock()
				defer mu.Unlock()
				i := min(calls[method], n-1)
				calls[method]++
				return i
			}
			_, client := newFakeRPC(t, map[string]func([]json.RawMessage) any{
				"getSignatureStatuses": func([]json.RawMessage) any {
					return withContext([]any{tt.statuses[next("status", len(tt.statuses))]})
				},
				"getBlockHeight": func([]json.RawMessage) any {
					return tt.heights[next("height", len(tt.heights))]
				},
			})
			ctx, cancel := context.WithTimeout(t.Context(), cmp(tt.timeout, 5*time.Second))
			defer cancel()

			err := confirm(ctx, client, sol.Signature{1}, lastValid, time.Millisecond, io.Discard)
			if tt.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.want)
			}
			if tt.timeout != 0 {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
			if tt.statusCalls != 0 {
				mu.Lock()
				defer mu.Unlock()
				assert.Equal(t, tt.statusCalls, calls["status"], "a processed status is not a confirmation")
			}
		})
	}
}

// cmp returns d, or def if d is zero.
func cmp(d, def time.Duration) time.Duration {
	if d == 0 {
		return def
	}
	return d
}

func TestConfirmRetriesFailedChecks(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	_, client := newFakeRPC(t, map[string]func([]json.RawMessage) any{
		"getSignatureStatuses": func([]json.RawMessage) any {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 1 {
				return withContext([]any{}) // a malformed answer counts as a failed check
			}
			return withContext([]any{map[string]any{"slot": 5, "err": nil, "confirmationStatus": "finalized"}})
		},
		"getBlockHeight": func([]json.RawMessage) any { return 1 },
	})
	var log bytes.Buffer
	require.NoError(t, confirm(t.Context(), client, sol.Signature{1}, 100, time.Millisecond, &log))
	assert.Contains(t, log.String(), "Status check failed, retrying")
}

func TestRunUsage(t *testing.T) {
	url := order(t).URL()
	tests := map[string]struct {
		args []string
		code int
		want string
	}{
		"help":                      {[]string{"-h"}, 0, "Usage:"},
		"no arguments":              {nil, 2, "want one solana: URL"},
		"two URLs":                  {[]string{"-keypair", "k.json", url, url}, 2, "want one solana: URL"},
		"no keypair":                {[]string{url}, 2, "-keypair is required"},
		"new keypair and a URL":     {[]string{"-new-keypair", "k.json", url}, 2, "-new-keypair takes no URL"},
		"new keypair and a keypair": {[]string{"-new-keypair", "k.json", "-keypair", "p.json"}, 2, "-new-keypair takes no URL"},
		"unknown flag":              {[]string{"-amount", "1"}, 2, "flag provided but not defined"},
		"bad URL":                   {[]string{"-keypair", "k.json", "solana:" + addr("merchant") + "?amount=1e3"}, 1, "payment URL: amount"},
		"missing keypair file":      {[]string{"-keypair", filepath.Join(t.TempDir(), "none.json"), url}, 1, "devpay -new-keypair creates one"},
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
	assert.Contains(t, stderr.String(), "https://faucet.solana.com")
	assert.NotContains(t, stderr.String()+stdout.String(), key.String(), "the secret key is never printed")
}

func TestRunPays(t *testing.T) {
	keypair := filepath.Join(t.TempDir(), "payer.json")
	_, err := writeKeypair(keypair)
	require.NoError(t, err)

	handlers, sent := devnet(t, 2_000_000)
	f, _ := newFakeRPC(t, handlers)
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, run([]string{"-keypair", keypair, "-rpc", f.url, order(t).URL()}, &stdout, &stderr), stderr.String())
	require.NotNil(t, sent.get())
	assert.Equal(t, sent.get().Signatures[0].String()+"\n", stdout.String(), "stdout is the signature alone")
	assert.Contains(t, stderr.String(), "Confirmed.")

	handlers["getGenesisHash"] = func([]json.RawMessage) any { return addr("another cluster") }
	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 1, run([]string{"-keypair", keypair, "-rpc", f.url, order(t).URL()}, &stdout, &stderr))
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "not on devnet")
}
