package solana

import (
	"context"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gagliardetto/solana-go/rpc/jsonrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

// countingRPC is a JSON-RPC client that answers at once and records when each call arrived.
type countingRPC struct{ at []time.Time }

func (c *countingRPC) CallForInto(context.Context, any, string, []any) error {
	c.at = append(c.at, time.Now())
	return nil
}

func (c *countingRPC) CallWithCallback(context.Context, string, []any, func(*http.Request, *http.Response) error) error {
	c.at = append(c.at, time.Now())
	return nil
}

func (c *countingRPC) CallBatch(context.Context, jsonrpc.RPCRequests) (jsonrpc.RPCResponses, error) {
	c.at = append(c.at, time.Now())
	return nil, nil
}

func TestLimiterSpacesEveryCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := &countingRPC{}
		l := limited{inner, rate.NewLimiter(rate.Every(callEvery), 1)}
		start := time.Now()

		require.NoError(t, l.CallForInto(t.Context(), nil, "getSignaturesForAddress", nil))
		require.NoError(t, l.CallWithCallback(t.Context(), "getTransaction", nil, nil))
		_, err := l.CallBatch(t.Context(), nil)
		require.NoError(t, err)
		require.NoError(t, l.CallForInto(t.Context(), nil, "getGenesisHash", nil))

		want := []time.Time{start, start.Add(callEvery), start.Add(2 * callEvery), start.Add(3 * callEvery)}
		assert.Equal(t, want, inner.at, "one call per 1.25 s, whatever the method")
	})
}

func TestLimiterGivesUpWithTheContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := &countingRPC{}
		l := limited{inner, rate.NewLimiter(rate.Every(callEvery), 1)}
		require.NoError(t, l.CallForInto(t.Context(), nil, "getGenesisHash", nil))

		ctx, cancel := context.WithTimeout(t.Context(), callEvery/2)
		defer cancel()
		require.Error(t, l.CallForInto(ctx, nil, "getGenesisHash", nil))
		assert.Len(t, inner.at, 1, "a call that cannot get a slot in time is not sent")
	})
}
