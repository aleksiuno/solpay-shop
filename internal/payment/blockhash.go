package payment

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/aleksiuno/solpay-shop/internal/solana"
)

// Blockhasher reads the newest blockhash, in base58, from the Solana RPC node. *solana.Client is
// one.
type Blockhasher interface {
	LatestBlockhash(ctx context.Context) (string, error)
}

// NoBlockhashError says that the RPC node gave no blockhash, so no transaction can be built, and
// when the cache asks the node again. Err is the node's error.
type NoBlockhashError struct {
	Err        error
	RetryAfter time.Duration // until the cache asks the node again; 0 if at the next request
}

func (e *NoBlockhashError) Error() string {
	return "payment: no blockhash from the RPC node: " + e.Err.Error()
}

func (e *NoBlockhashError) Unwrap() error { return e.Err }

const (
	// blockhashMaxAge is how long a blockhash is handed out, from the start of its fetch. The
	// cluster takes a transaction for about 60 to 90 s after its blockhash, so a wallet has at
	// least 50 s to sign; and while the node answers, the shop makes at most one call per 10 s of
	// the eight that the client's rate limiter shares with the watcher.
	blockhashMaxAge = 10 * time.Second
	// blockhashTimeout bounds one fetch, the wait for the rate limiter included, as
	// Config.RPCTimeout bounds each call of the watcher.
	blockhashTimeout = 10 * time.Second
	// blockhashRetry is how long after a failed fetch began the next one starts, at the soonest:
	// two turns of the rate limiter, so that while the node fails, the cache starts at most four
	// of the limiter's eight calls per 10 s and leaves the rest to the watcher.
	blockhashRetry = 2500 * time.Millisecond
	// rateLimitBackoff is how long after the node rate-limited a fetch no other starts. It is also
	// the least the watcher waits by default after a tick that the node rate-limited.
	rateLimitBackoff = 15 * time.Second
)

// blockhashCache hands out the newest blockhash, fetched at most once per blockhashMaxAge.
// Callers that find a fetch under way wait for it. A failed fetch is not kept: the next caller
// waits for a new one, which starts blockhashRetry after the failed one began. After a rate-limited
// fetch, none starts for rateLimitBackoff, and callers meanwhile get its error at once. It is safe
// for concurrent use.
type blockhashCache struct {
	node Blockhasher
	now  func() time.Time
	log  *slog.Logger

	mu      sync.Mutex
	hash    string    // the last blockhash fetched, "" before the first
	fetched time.Time // by now, when the fetch of hash began
	pending *call     // the fetch under way, or nil
	// After a failure, no fetch starts until pause has passed since paused.
	paused  time.Time
	pause   time.Duration
	limited error // the node's error while a rate limit pauses the fetches, or nil
}

// call is one fetch, which every caller that needs it waits for.
type call struct {
	done chan struct{} // closed once hash and err are set
	hash string
	err  error
}

// get returns a blockhash whose fetch began less than blockhashMaxAge ago, fetching one if need
// be. A failed fetch gives a *NoBlockhashError. If ctx ends first, get returns ctx's error, and
// the fetch goes on for the other callers.
func (c *blockhashCache) get(ctx context.Context) (string, error) {
	c.mu.Lock()
	now := c.now()
	// A negative age means that the clock stepped back: the blockhash may be of any age.
	if age := now.Sub(c.fetched); c.hash != "" && age >= 0 && age < blockhashMaxAge {
		hash := c.hash
		c.mu.Unlock()
		return hash, nil
	}
	f := c.pending
	if f == nil {
		var wait time.Duration
		if since := now.Sub(c.paused); since >= 0 && since < c.pause {
			wait = c.pause - since
		}
		if c.limited != nil && wait > 0 {
			err := &NoBlockhashError{Err: c.limited, RetryAfter: wait}
			c.mu.Unlock()
			return "", err
		}
		f = &call{done: make(chan struct{})}
		c.pending = f
		// The fetch serves every caller that waits for it, so no one caller's end cancels it.
		go c.fetch(context.WithoutCancel(ctx), f, wait)
	}
	c.mu.Unlock()
	select {
	case <-f.done:
		return f.hash, f.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// fetch makes f's call to the node once wait has passed, and keeps the blockhash if it gets one.
// If not, it pauses the fetches, and logs the node's error once for all of f's callers.
func (c *blockhashCache) fetch(ctx context.Context, f *call, wait time.Duration) {
	time.Sleep(wait)
	ctx, cancel := context.WithTimeout(ctx, blockhashTimeout)
	defer cancel()
	start := c.now()
	hash, err := c.node.LatestBlockhash(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = nil
	defer close(f.done)
	if err == nil {
		f.hash, c.hash, c.fetched = hash, hash, start
		return
	}
	end := c.now()
	c.paused, c.pause, c.limited = start, blockhashRetry, nil
	if errors.Is(err, solana.ErrRateLimited) { // the node asks the shop to back off
		c.paused, c.pause, c.limited = end, rateLimitBackoff, err
	}
	e := &NoBlockhashError{Err: err, RetryAfter: max(c.pause-end.Sub(c.paused), 0)}
	f.err = e
	c.log.LogAttrs(ctx, slog.LevelError, "no blockhash from the RPC node",
		slog.Any("error", err), slog.Duration("retry_after", e.RetryAfter))
}
