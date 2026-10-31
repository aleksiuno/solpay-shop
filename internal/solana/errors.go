package solana

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gagliardetto/solana-go/rpc"
	"github.com/gagliardetto/solana-go/rpc/jsonrpc"
)

// Every error that Signatures and Transaction return falls into one of five classes, and
// CheckDevnet adds a sixth, ErrNotDevnet. A context canceled or expired before the answer passes
// through, so errors.Is finds context.Canceled or context.DeadlineExceeded. Three of the five have a sentinel
// below, as does ErrNotDevnet. Any other error is transient, such as a refused connection, a 5xx
// answer or a reply that solana-go cannot parse, and the same call may work on the next tick. The
// original error stays in the chain, so errors.As still finds a *jsonrpc.RPCError or
// *jsonrpc.HTTPError.
var (
	// ErrNotFound: the node returned null for a transaction it has not indexed yet. Retry later.
	ErrNotFound = errors.New("solana: not found")
	// ErrRateLimited: HTTP 429, as a JSON-RPC error with code 429 or as a plain HTTP answer.
	// Back off.
	ErrRateLimited = errors.New("solana: rate limited")
	// ErrPermanent: asking again cannot help. The transaction is in a version the client does not
	// accept (-32015); the result parses but does not hold a consistent transaction (bad wire
	// bytes, an index out of range, balances that do not match the keys, a number above int64);
	// the signature or reference asked for is malformed; or the node returned another transaction.
	ErrPermanent = errors.New("solana: permanent failure")
	// ErrNotDevnet: the node's genesis hash is not devnet's. Nothing may be polled.
	ErrNotDevnet = errors.New("solana: not devnet")
)

// codeUnsupportedVersion is the JSON-RPC error for a transaction in a newer version than the
// request accepts.
const codeUnsupportedVersion = -32015

// classify wraps err in the sentinel of its class, or returns it unchanged for the context and
// transient classes.
func classify(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, rpc.ErrNotFound) {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	if re, ok := errors.AsType[*jsonrpc.RPCError](err); ok {
		switch re.Code {
		case http.StatusTooManyRequests:
			return fmt.Errorf("%w: %w", ErrRateLimited, err)
		case codeUnsupportedVersion:
			return fmt.Errorf("%w: %w", ErrPermanent, err)
		}
		return err
	}
	if he, ok := errors.AsType[*jsonrpc.HTTPError](err); ok && he.Code == http.StatusTooManyRequests {
		return fmt.Errorf("%w: %w", ErrRateLimited, err)
	}
	return err
}
