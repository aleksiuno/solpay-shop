package solana

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	sol "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/gagliardetto/solana-go/rpc/jsonrpc"
	"golang.org/x/time/rate"
)

const (
	// callEvery spaces all calls, whatever the method. The public devnet endpoint allows about ten
	// calls per method per ten seconds and counts rejected calls too, so one call per 1.25 s keeps
	// the shop at eight and leaves room for a block explorer on the same IP address.
	callEvery = 1250 * time.Millisecond
	// requestTimeout bounds one HTTP request when the caller's context sets no earlier deadline.
	requestTimeout = 30 * time.Second
)

// Client reads payments from a Solana JSON-RPC endpoint and is safe for concurrent use. All its
// calls share one rate limiter.
type Client struct {
	rpc *rpc.Client
}

// New returns a client for the endpoint. The error never repeats the URL.
func New(endpoint string) (*Client, error) {
	if u, err := url.Parse(endpoint); err != nil || u.Host == "" {
		return nil, errors.New("solana: the RPC endpoint is not a valid URL")
	}
	hc := &http.Client{Timeout: requestTimeout}
	jc := jsonrpc.NewClientWithOpts(endpoint, &jsonrpc.RPCClientOpts{HTTPClient: hc})
	return &Client{rpc: rpc.NewWithCustomRPCClient(limited{jc, rate.NewLimiter(rate.Every(callEvery), 1)})}, nil
}

// Signatures lists up to limit transactions that mention the reference, newest first, at
// confirmed commitment.
func (c *Client) Signatures(ctx context.Context, reference string, limit int) ([]SignatureInfo, error) {
	addr, err := sol.PublicKeyFromBase58(reference)
	if err != nil {
		return nil, fmt.Errorf("%w: reference %q: %w", ErrPermanent, reference, err)
	}
	out, err := c.rpc.GetSignaturesForAddressWithOpts(ctx, addr, &rpc.GetSignaturesForAddressOpts{
		Limit:      &limit,
		Commitment: rpc.CommitmentConfirmed,
	})
	if err != nil {
		return nil, classify(err)
	}
	if out == nil { // [] decodes to an empty slice; nil means the reply had no result at all
		return nil, errors.New("solana: getSignaturesForAddress returned no result")
	}
	sigs := make([]SignatureInfo, 0, len(out))
	for i, s := range out {
		if s == nil {
			return nil, fmt.Errorf("%w: signature entry %d is null", ErrPermanent, i)
		}
		slot, err := toInt64(s.Slot)
		if err != nil {
			return nil, fmt.Errorf("%w: signature %s slot: %w", ErrPermanent, s.Signature, err)
		}
		sigs = append(sigs, SignatureInfo{Signature: s.Signature.String(), Slot: slot, BlockTime: blockTime(s.BlockTime), Failed: s.Err != nil})
	}
	return sigs, nil
}

// Transaction fetches a transaction at confirmed commitment, in any version up to v1.
func (c *Client) Transaction(ctx context.Context, signature string) (Tx, error) {
	sig, err := sol.SignatureFromBase58(signature)
	if err != nil {
		return Tx{}, fmt.Errorf("%w: signature %q: %w", ErrPermanent, signature, err)
	}
	maxVersion := rpc.MaxSupportedTransactionVersion1
	res, err := c.rpc.GetTransaction(ctx, sig, &rpc.GetTransactionOpts{
		Encoding:                       sol.EncodingBase64,
		Commitment:                     rpc.CommitmentConfirmed,
		MaxSupportedTransactionVersion: &maxVersion,
	})
	if err != nil {
		return Tx{}, classify(err)
	}
	tx, err := toTx(res)
	if err != nil {
		return Tx{}, fmt.Errorf("%w: %s: %w", ErrPermanent, signature, err)
	}
	if tx.Signature != signature {
		return Tx{}, fmt.Errorf("%w: asked for %s, got %s", ErrPermanent, signature, tx.Signature)
	}
	return tx, nil
}

// CheckDevnet returns ErrNotDevnet unless the node's genesis hash is devnet's.
func (c *Client) CheckDevnet(ctx context.Context) error {
	hash, err := c.rpc.GetGenesisHash(ctx)
	if err != nil {
		return classify(err)
	}
	if hash.String() != DevnetGenesisHash {
		return fmt.Errorf("%w: genesis hash %s", ErrNotDevnet, hash)
	}
	return nil
}

// limited makes every call wait for the shared rate limiter first.
type limited struct {
	rpc.JSONRPCClient
	lim *rate.Limiter
}

func (l limited) CallForInto(ctx context.Context, out any, method string, params []any) error {
	if err := l.lim.Wait(ctx); err != nil {
		return err
	}
	return l.JSONRPCClient.CallForInto(ctx, out, method, params)
}

func (l limited) CallWithCallback(ctx context.Context, method string, params []any, callback func(*http.Request, *http.Response) error) error {
	if err := l.lim.Wait(ctx); err != nil {
		return err
	}
	return l.JSONRPCClient.CallWithCallback(ctx, method, params, callback)
}

func (l limited) CallBatch(ctx context.Context, requests jsonrpc.RPCRequests) (jsonrpc.RPCResponses, error) {
	if err := l.lim.Wait(ctx); err != nil {
		return nil, err
	}
	return l.JSONRPCClient.CallBatch(ctx, requests)
}
