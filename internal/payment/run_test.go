package payment_test

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
	"github.com/aleksiuno/solpay-shop/internal/solana"
	"github.com/aleksiuno/solpay-shop/internal/solana/solanatest"
)

// newRunRig returns a rig for Run, called in a synctest bubble: the watcher's clock is the
// bubble's.
func newRunRig(t *testing.T, c payment.Config) *rig {
	r := newRig(t, c)
	rec := audit.NewRecorder(r.store, slog.New(slog.DiscardHandler))
	r.w = payment.NewWatcher(r.chain, r.store, rec, merchant, c, time.Now)
	return r
}

// orderNow stores a pending order like order does, placed now, with its deadline a day away so
// that it stays pending however long a test runs.
func (r *rig) orderNow() order.Order {
	o := r.order()
	p := r.store.find(o.ID)
	now := time.Now()
	p.CreatedAt, p.ExpiresAt, p.next = now, now.Add(24*time.Hour), new(now)
	return p.Order
}

// run starts Run in the background and waits until it blocks: after its first tick, or during it
// when the node or the store makes it wait. stop cancels Run and returns what it returned.
func (r *rig) run() (stop func() error) {
	ctx, cancel := context.WithCancel(r.t.Context())
	done := make(chan error, 1)
	go func() { done <- r.w.Run(ctx) }()
	synctest.Wait()
	return func() error {
		cancel()
		return <-done
	}
}

// polls returns how many polls the node has seen.
func (r *rig) polls() int {
	return r.chain.Calls(solanatest.GetSignaturesForAddress)
}

// after requires the next ticks, which poll n orders, to come exactly d after the last.
func (r *rig) after(d time.Duration, n int) {
	r.t.Helper()
	r.afterCounting(d, r.polls, n)
}

// afterCounting requires the next ticks to come exactly d after the last: count, which counts
// what ticks do, stays as it is until just before d has passed, and grows by n at d.
func (r *rig) afterCounting(d time.Duration, count func() int, n int) {
	r.t.Helper()
	before := count()
	time.Sleep(d - time.Nanosecond)
	synctest.Wait()
	require.Equal(r.t, before, count(), "no tick before %s", d)
	time.Sleep(time.Nanosecond)
	synctest.Wait()
	require.Equal(r.t, before+n, count(), "a tick %s later", d)
}

// After failed ticks, Run waits PollInterval doubled for each in a row, up to MaxBackoff, and at
// least RateLimitBackoff after a 429. A tick that succeeds ends the backoff.
func TestRunBacksOff(t *testing.T) {
	limited := fmt.Errorf("%w: HTTP 429", solana.ErrRateLimited)
	// Forty failures run past the point where doubling 5 s would overflow.
	outage := append(slices.Repeat([]error{errBoom}, 40), nil)
	outageWaits := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second}
	outageWaits = append(outageWaits, slices.Repeat([]time.Duration{time.Minute}, 37)...)
	tests := []struct {
		name     string
		interval time.Duration
		errs     []error         // of each tick in turn; nil for one that succeeds
		waits    []time.Duration // after each tick
	}{
		{"outage", 5 * time.Second, outage, append(outageWaits, 5*time.Second)},
		// At the shortest interval it takes six doublings to reach MaxBackoff.
		{"outage at 1 s", time.Second, append(slices.Repeat([]error{errBoom}, 7), nil), []time.Duration{
			2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second,
			time.Minute, time.Minute, time.Second,
		}},
		{"rate limited", 5 * time.Second, []error{limited, limited, nil, errBoom, nil}, []time.Duration{
			15 * time.Second, 20 * time.Second, 5 * time.Second, 10 * time.Second, 5 * time.Second,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := cfg
				c.PollInterval = tt.interval
				r := newRunRig(t, c)
				r.orderNow()
				fail := func(tick int) {
					if tick < len(tt.errs) && tt.errs[tick] != nil {
						r.chain.FailNext(solanatest.GetSignaturesForAddress, tt.errs[tick])
					}
				}
				fail(0)
				stop := r.run()
				require.Equal(t, 1, r.polls())
				for i, wait := range tt.waits {
					fail(i + 1)
					r.after(wait, 1)
				}
				require.NoError(t, stop())
			})
		})
	}
}

// A tick that polls a full batch is followed at once by the next; after a shorter one, the next
// starts PollInterval after it began.
func TestRunTakesTheNextBatchAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cfg
		c.Batch = 2
		r := newRunRig(t, c)
		for range 3 {
			r.orderNow()
		}
		stop := r.run()
		require.Equal(t, 3, r.polls(), "a full batch, and at once the rest")
		r.after(c.PollInterval, 3)
		require.NoError(t, stop())
		assert.Equal(t, 4, r.store.called("WatchSet"), "two ticks each time, the second one short")
	})
}

// After a tick that polls nothing, the next starts PollInterval after it, and only one.
func TestRunWaitsWhenIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRunRig(t, cfg)
		stop := r.run()
		ticks := func() int { return r.store.called("WatchSet") }
		for range 3 {
			r.afterCounting(cfg.PollInterval, ticks, 1)
		}
		require.NoError(t, stop())
	})
}

// slowChain is a node that takes d to answer the devnet check and each listing.
type slowChain struct {
	*solanatest.Fake
	d time.Duration
}

func (c slowChain) CheckDevnet(ctx context.Context) error {
	time.Sleep(c.d)
	return c.Fake.CheckDevnet(ctx)
}

func (c slowChain) Signatures(ctx context.Context, reference string, limit int) ([]solana.SignatureInfo, error) {
	sigs, err := c.Fake.Signatures(ctx, reference, limit)
	time.Sleep(c.d)
	return sigs, err
}

// After a short tick, the next starts PollInterval after this one began polling, when the orders
// it polled are due again, so a slow node does not stretch the time between polls.
func TestRunKeepsThePollInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRunRig(t, cfg)
		rec := audit.NewRecorder(r.store, slog.New(slog.DiscardHandler))
		r.w = payment.NewWatcher(slowChain{r.chain, time.Second}, r.store, rec, merchant, cfg, time.Now)
		r.orderNow()
		r.orderNow()
		// The first tick checks the cluster for 1 s, then polls the two orders at 1 s and 2 s.
		stop := r.run()
		time.Sleep(time.Second + cfg.PollInterval - time.Nanosecond)
		synctest.Wait()
		require.Equal(t, 2, r.polls())
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		assert.Equal(t, 3, r.polls(), "the next tick, PollInterval after the first began polling")
		require.NoError(t, stop())
	})
}

// Run ends with nil once canceled, during a tick or between ticks. It records when it started
// and stopped, and records no failure for a tick that the cancel cut short.
func TestRunStops(t *testing.T) {
	for _, during := range []string{"tick", "wait"} {
		t.Run(during, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newRunRig(t, cfg)
				r.orderNow()
				if during == "tick" {
					r.chain.BlockUntilDone(solanatest.GetSignaturesForAddress)
				}
				stop := r.run()
				require.NoError(t, stop())
				assert.Equal(t, []audit.Event{
					{Kind: audit.WatcherStarted, Message: "watcher started", Data: map[string]any{
						"poll_interval": "5s", "batch": 8, "late_window": "1h0m0s",
					}},
					{Kind: audit.WatcherStopped, Message: "watcher stopped"},
				}, r.store.events)
			})
		})
	}
}

// A node on another cluster ends Run with solana.ErrNotDevnet before any poll.
func TestRunStopsOnAnotherCluster(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRunRig(t, cfg)
		r.orderNow()
		r.chain.FailNext(solanatest.GetGenesisHash, fmt.Errorf("%w: genesis hash x", solana.ErrNotDevnet))

		require.ErrorIs(t, r.w.Run(t.Context()), solana.ErrNotDevnet)
		assert.Zero(t, r.polls())
		require.Len(t, r.store.events, 2)
		assert.Equal(t, audit.WatcherStopped, r.store.events[1].Kind)
	})
}

// cancelingChain is a node on another cluster whose answer comes as the watcher is canceled.
type cancelingChain struct {
	*solanatest.Fake
	cancel context.CancelFunc
}

func (c cancelingChain) CheckDevnet(context.Context) error {
	c.cancel()
	return fmt.Errorf("%w: genesis hash x", solana.ErrNotDevnet)
}

// A not-devnet answer is returned even when Run is canceled as it comes, so the caller still
// learns that the node is on another cluster.
func TestRunReportsAnotherClusterWhileStopping(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRunRig(t, cfg)
		ctx, cancel := context.WithCancel(t.Context())
		rec := audit.NewRecorder(r.store, slog.New(slog.DiscardHandler))
		w := payment.NewWatcher(cancelingChain{r.chain, cancel}, r.store, rec, merchant, cfg, time.Now)

		require.ErrorIs(t, w.Run(ctx), solana.ErrNotDevnet)
	})
}
