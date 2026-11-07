package payment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Blockhasher reads the newest blockhash, in base58, from the Solana RPC node. *solana.Client is
// one.
type Blockhasher interface {
	LatestBlockhash(ctx context.Context) (string, error)
}

// ErrNoBlockhash: the RPC node gave no blockhash, so no transaction can be built. Asking again
// later may work.
var ErrNoBlockhash = errors.New("payment: no blockhash from the RPC node")

const (
	// blockhashMaxAge is how long a blockhash is handed out, from the start of its fetch. The
	// cluster takes a transaction for about 60 to 90 s after its blockhash, so a wallet has at
	// least 50 s to sign; and while the node answers, the shop makes at most one call per 10 s of
	// the eight that the client's rate limiter shares with the watcher.
	blockhashMaxAge = 10 * time.Second
	// blockhashTimeout bounds one fetch, the wait for the rate limiter included, as
	// Config.RPCTimeout bounds each call of the watcher.
	blockhashTimeout = 10 * time.Second
)

// blockhashCache hands out the newest blockhash, fetched at most once per blockhashMaxAge.
// Callers that find a fetch under way wait for it. A failed fetch is not kept, so the next caller
// starts another. It is safe for concurrent use.
type blockhashCache struct {
	node Blockhasher
	now  func() time.Time
	log  *slog.Logger

	mu      sync.Mutex
	hash    string    // the last blockhash fetched, "" before the first
	fetched time.Time // by now, when the fetch of hash began
	pending *call     // the fetch under way, or nil
}

// call is one fetch, which every caller that needs it waits for.
type call struct {
	done chan struct{} // closed once hash and err are set
	hash string
	err  error
}

// get returns a blockhash whose fetch began less than blockhashMaxAge ago, fetching one if need
// be. A failed fetch gives an error wrapping ErrNoBlockhash. If ctx ends first, get returns ctx's
// error, and the fetch goes on for the other callers.
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
		f = &call{done: make(chan struct{})}
		c.pending = f
		// The fetch serves every caller that waits for it, so no one caller's end cancels it.
		go c.fetch(context.WithoutCancel(ctx), f, now)
	}
	c.mu.Unlock()
	select {
	case <-f.done:
		return f.hash, f.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// fetch makes f's call to the node, begun at start, and keeps the blockhash if it gets one. If
// not, it logs the node's error, once for all of f's callers.
func (c *blockhashCache) fetch(ctx context.Context, f *call, start time.Time) {
	ctx, cancel := context.WithTimeout(ctx, blockhashTimeout)
	defer cancel()
	hash, err := c.node.LatestBlockhash(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		f.err = fmt.Errorf("%w: %w", ErrNoBlockhash, err)
		c.log.LogAttrs(ctx, slog.LevelError, "no blockhash from the RPC node", slog.Any("error", err))
	} else {
		f.hash, c.hash, c.fetched = hash, hash, start
	}
	c.pending = nil
	close(f.done)
}
