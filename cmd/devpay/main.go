// Command devpay pays a Solana Pay transfer-request URL on devnet. It attaches the URL's references
// to the transfer as read-only accounts, as Solana Pay wallets do, so the shop can detect the
// payment; `solana transfer` cannot attach them. devpay refuses to pay on any cluster but devnet.
//
// Usage:
//
//	devpay -new-keypair data/payer.json
//	devpay -keypair data/payer.json [-rpc URL] 'solana:<recipient>?amount=<SOL>&reference=<address>'
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	sol "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/gagliardetto/solana-go/rpc/jsonrpc"

	"github.com/aleksiuno/solpay-shop/internal/money"
	"github.com/aleksiuno/solpay-shop/internal/solana"
	"github.com/aleksiuno/solpay-shop/internal/solanapay"
)

const (
	defaultRPC = "https://api.devnet.solana.com"
	faucetURL  = "https://faucet.solana.com"

	// A payment's blockhash expires about a minute after devpay fetches it, so confirmation
	// ends well within payTimeout. Two checks every pollEvery stay far below the public
	// endpoint's limit of ten calls per method per ten seconds.
	payTimeout = 2 * time.Minute
	rpcTimeout = 30 * time.Second
	pollEvery  = 2 * time.Second
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses the command line and returns the exit code: 0 on success, 1 on failure and 2 on a
// usage error. Results go to stdout (an address or a signature) and messages to stderr.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("devpay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keypair := fs.String("keypair", "", "solana-keygen `file` of the wallet that pays")
	newKeypair := fs.String("new-keypair", "", "create a keypair `file` (mode 0600), print its address and exit")
	rpcURL := fs.String("rpc", defaultRPC, "devnet JSON-RPC `URL`")
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage:
  devpay -new-keypair FILE
  devpay -keypair FILE [-rpc URL] 'solana:<recipient>?amount=<SOL>&reference=<address>'

devpay pays a Solana Pay transfer-request URL on devnet, with the URL's
references attached so the shop can detect the payment. Quote the URL: it
contains '&'. devpay checks the RPC node's genesis hash and pays on devnet only.

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if *newKeypair != "" {
		if fs.NArg() != 0 || *keypair != "" {
			return usageError(stderr, "-new-keypair takes no URL and no -keypair")
		}
		addr, err := writeKeypair(*newKeypair)
		if err != nil {
			fmt.Fprintf(stderr, "devpay: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, addr)
		fmt.Fprintf(stderr, "Wrote a new keypair to %s, readable only by you. It holds the wallet's secret key:\n"+
			"keep it out of git and use it on devnet only. To pay from this wallet, get devnet SOL\n"+
			"for the address above at %s.\n", *newKeypair, faucetURL)
		return 0
	}

	if fs.NArg() != 1 {
		return usageError(stderr, "want one solana: URL")
	}
	if *keypair == "" {
		return usageError(stderr, "-keypair is required: the wallet that pays")
	}
	req, err := solanapay.Parse(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "devpay: payment URL: %v\n", err)
		return 1
	}
	key, err := sol.PrivateKeyFromSolanaKeygenFile(*keypair)
	if err != nil {
		fmt.Fprintf(stderr, "devpay: %v (devpay -new-keypair creates one)\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	context.AfterFunc(ctx, stop) // after the first signal, a second one kills devpay
	ctx, cancel := context.WithTimeout(ctx, payTimeout)
	defer cancel()
	sig, err := pay(ctx, rpc.NewWithTimeout(*rpcURL, rpcTimeout), key, req, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "devpay: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, sig)
	fmt.Fprintln(stderr, "Confirmed.")
	return 0
}

func usageError(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "devpay: %s (devpay -h shows the usage)\n", msg)
	return 2
}

// writeKeypair creates a keypair and writes it to path in the solana-keygen format, a JSON array
// of the 64 secret-key bytes, readable only by the user. It creates the directory if needed and
// never overwrites a file.
func writeKeypair(path string) (sol.PublicKey, error) {
	key, err := sol.NewRandomPrivateKey()
	if err != nil {
		return sol.PublicKey{}, fmt.Errorf("generate keypair: %w", err)
	}
	ints := make([]int, len(key)) // a []byte would marshal as base64
	for i, b := range key {
		ints[i] = int(b)
	}
	data, err := json.Marshal(ints)
	if err != nil {
		return sol.PublicKey{}, fmt.Errorf("encode keypair: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return sol.PublicKey{}, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return sol.PublicKey{}, err // the error names the file; it says "file exists" for an existing one
	}
	_, err = f.Write(data)
	if err = errors.Join(err, f.Close()); err != nil {
		_ = os.Remove(path) // a partial key file is useless
		return sol.PublicKey{}, fmt.Errorf("write %s: %w", path, err)
	}
	return key.PublicKey(), nil
}

// pay sends the transfer that req asks for from the wallet of key and waits until the cluster
// confirms it. It checks everything it can before sending: the request, that the RPC node is on
// devnet and that the wallet holds the amount. Progress goes to log.
func pay(ctx context.Context, client *rpc.Client, key sol.PrivateKey, req solanapay.Request, log io.Writer) (sol.Signature, error) {
	payer := key.PublicKey()
	inst, err := transfer(req, payer)
	if err != nil {
		return sol.Signature{}, err
	}
	if err := checkDevnet(ctx, client); err != nil {
		return sol.Signature{}, err
	}

	balance, err := client.GetBalance(ctx, payer, rpc.CommitmentConfirmed)
	if err != nil {
		return sol.Signature{}, fmt.Errorf("get the payer's balance: %w", err)
	}
	if have := balance.Value; have <= math.MaxInt64 && int64(have) < req.Lamports {
		return sol.Signature{}, fmt.Errorf("the payer %s holds %s SOL, and the payment needs %s SOL plus a small fee: get devnet SOL at %s",
			payer, money.FormatSOL(int64(have)), money.FormatSOL(req.Lamports), faucetURL)
	}

	latest, err := client.GetLatestBlockhash(ctx, rpc.CommitmentConfirmed)
	if err != nil {
		return sol.Signature{}, fmt.Errorf("get a recent blockhash: %w", err)
	}
	tx, err := sol.NewTransaction([]sol.Instruction{inst}, latest.Value.Blockhash, sol.TransactionPayer(payer))
	if err != nil {
		return sol.Signature{}, fmt.Errorf("build the transaction: %w", err)
	}
	if _, err := tx.Sign(func(k sol.PublicKey) *sol.PrivateKey {
		if k.Equals(payer) {
			return &key
		}
		return nil
	}); err != nil {
		return sol.Signature{}, fmt.Errorf("sign the transaction: %w", err)
	}

	// The first signature identifies the transaction, so it is shown before the send: if the send
	// fails in transit, the node may have received the transaction all the same.
	sig := tx.Signatures[0]
	fmt.Fprintf(log, "Paying %s SOL from %s to %s on devnet.\nTransaction: https://explorer.solana.com/tx/%s?cluster=devnet\n",
		money.FormatSOL(req.Lamports), payer, req.Recipient, sig)
	if _, err := client.SendTransactionWithOpts(ctx, tx, rpc.TransactionOpts{PreflightCommitment: rpc.CommitmentConfirmed}); err != nil {
		if _, refused := errors.AsType[*jsonrpc.RPCError](err); refused {
			return sol.Signature{}, fmt.Errorf("send the transaction: %w", err) // the node refused it: nothing was sent
		}
		return sol.Signature{}, fmt.Errorf("send the transaction: %w; the node may have received it, so look the transaction up before paying again", err)
	}
	fmt.Fprintln(log, "Sent; waiting for confirmation.")
	if err := confirm(ctx, client, sig, latest.Value.LastValidBlockHeight, pollEvery, log); err != nil {
		return sol.Signature{}, err
	}
	return sig, nil
}

// transfer builds the System Program transfer that pays req from payer. The request's references
// follow the payer and the recipient as read-only, non-signer accounts, in the URL's order, as
// Solana Pay specifies; the System Program ignores the extra accounts. It refuses a request the
// shop could not detect or that would pay the payer itself.
func transfer(req solanapay.Request, payer sol.PublicKey) (sol.Instruction, error) {
	if len(req.References) == 0 {
		return nil, errors.New("the URL has no reference, so the shop could not detect this payment")
	}
	if req.Lamports <= 0 {
		return nil, errors.New("the URL names no amount")
	}
	recipient, err := sol.PublicKeyFromBase58(req.Recipient)
	if err != nil {
		return nil, fmt.Errorf("recipient: %w", err)
	}
	if recipient.Equals(payer) {
		return nil, errors.New("the payer is the recipient: pay from another wallet")
	}
	t := system.NewTransferInstruction(uint64(req.Lamports), payer, recipient)
	for _, ref := range req.References {
		k, err := sol.PublicKeyFromBase58(ref)
		if err != nil {
			return nil, fmt.Errorf("reference: %w", err)
		}
		t.Append(sol.Meta(k)) // neither writable nor a signer
	}
	return t.Build(), nil
}

// checkDevnet refuses any RPC node whose genesis hash is not devnet's: a Solana Pay URL names no
// cluster, and on mainnet the same transfer would move real SOL.
func checkDevnet(ctx context.Context, client *rpc.Client) error {
	hash, err := client.GetGenesisHash(ctx)
	if err != nil {
		return fmt.Errorf("get the cluster's genesis hash: %w", err)
	}
	if hash.String() != solana.DevnetGenesisHash {
		return fmt.Errorf("the RPC node is not on devnet (genesis hash %s), and devpay pays on devnet only", hash)
	}
	return nil
}

// confirm waits until sig is confirmed or fails on chain, or until it can no longer land: once
// the cluster's block height passes lastValid, the transaction's blockhash is no longer accepted.
// The height alone does not decide: the status is checked once more after the height has passed,
// in case the transaction landed in between. A failed status check is retried.
func confirm(ctx context.Context, client *rpc.Client, sig sol.Signature, lastValid uint64, every time.Duration, log io.Writer) error {
	expired := false
	for {
		st, err := status(ctx, client, sig)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				fmt.Fprintf(log, "Status check failed, retrying: %v\n", err)
			}
		case st == nil: // not seen by the node yet
			if expired {
				return errors.New("the transaction expired before it landed, so nothing was paid: run devpay again")
			}
		case st.Err != nil:
			return fmt.Errorf("the transaction failed on chain: %v", st.Err)
		case st.ConfirmationStatus == rpc.ConfirmationStatusConfirmed,
			st.ConfirmationStatus == rpc.ConfirmationStatusFinalized:
			return nil
		}
		if !expired {
			if height, err := client.GetBlockHeight(ctx, rpc.CommitmentConfirmed); err == nil && height > lastValid {
				expired = true
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("no confirmation yet (%w): look the transaction up before paying again", context.Cause(ctx))
		case <-time.After(every):
		}
	}
}

// status returns the status of sig, or nil if the node has not seen it.
func status(ctx context.Context, client *rpc.Client, sig sol.Signature) (*rpc.SignatureStatusesResult, error) {
	out, err := client.GetSignatureStatuses(ctx, false, sig)
	if err != nil {
		return nil, err
	}
	if len(out.Value) != 1 {
		return nil, fmt.Errorf("getSignatureStatuses returned %d statuses for one signature", len(out.Value))
	}
	return out.Value[0], nil
}
