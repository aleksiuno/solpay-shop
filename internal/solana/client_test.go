package solana_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go/rpc/jsonrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/solana"
	"github.com/aleksiuno/solpay-shop/internal/solana/solanatest"
)

// node is a JSON-RPC endpoint. It checks the method (when set) and the params (when set), then
// answers with result; or with status and body, when status is set; or with the first bytes of
// a body, after which it stalls until the client gives up, when stall is set.
type node struct {
	t      *testing.T
	method string
	params string // the expected params as JSON
	result any
	status int
	body   string
	delay  time.Duration
	stall  string
}

func (n *node) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		n.t.Errorf("decode the request: %v", err)
		return
	}
	if n.method != "" {
		assert.Equal(n.t, n.method, req.Method)
	}
	if n.params != "" {
		assert.JSONEq(n.t, n.params, string(req.Params))
	}
	if n.delay > 0 {
		select {
		case <-time.After(n.delay):
		case <-r.Context().Done():
			return
		}
	}
	switch {
	case n.stall != "":
		_, _ = io.WriteString(w, n.stall)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	case n.status != 0:
		w.WriteHeader(n.status)
		_, _ = io.WriteString(w, n.body)
	default:
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(n.t, json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": n.result}))
	}
}

// serve starts n and returns a client for it. Each client has its own rate limiter, so a test's
// first call is never delayed.
func serve(t *testing.T, n *node) *solana.Client {
	t.Helper()
	n.t = t
	srv := httptest.NewServer(n)
	t.Cleanup(srv.Close)
	c, err := solana.New(srv.URL)
	require.NoError(t, err)
	return c
}

// calls are the client's three calls, so that a test of the HTTP layer can run against each.
var calls = map[string]func(context.Context, *solana.Client) error{
	"Signatures": func(ctx context.Context, c *solana.Client) error {
		_, err := c.Signatures(ctx, solanatest.Key("reference"), 100)
		return err
	},
	"Transaction": func(ctx context.Context, c *solana.Client) error {
		_, err := c.Transaction(ctx, solanatest.Devpay.Signature)
		return err
	},
	"CheckDevnet": func(ctx context.Context, c *solana.Client) error { return c.CheckDevnet(ctx) },
}

func TestSignatures(t *testing.T) {
	ref := solanatest.Key("reference")
	c := serve(t, &node{
		method: "getSignaturesForAddress",
		params: `["` + ref + `", {"commitment": "confirmed", "limit": 100}]`,
		result: []any{
			map[string]any{"signature": solanatest.Devpay.Signature, "slot": 517259492, "blockTime": 1793479654, "err": nil, "confirmationStatus": "confirmed"},
			map[string]any{"signature": solanatest.Failed.Signature, "slot": 517261348, "blockTime": nil, "err": map[string]any{"InstructionError": []any{0, map[string]any{"Custom": 1}}}},
		},
	})

	sigs, err := c.Signatures(t.Context(), ref, 100)
	require.NoError(t, err)
	at := time.Unix(1793479654, 0).UTC()
	assert.Equal(t, []solana.SignatureInfo{
		{Signature: solanatest.Devpay.Signature, Slot: 517259492, BlockTime: &at},
		{Signature: solanatest.Failed.Signature, Slot: 517261348, Failed: true},
	}, sigs)

	c = serve(t, &node{method: "getSignaturesForAddress", result: []any{}})
	sigs, err = c.Signatures(t.Context(), ref, 100)
	require.NoError(t, err)
	assert.Empty(t, sigs, "a reference nobody paid yet")
}

func TestTransaction(t *testing.T) {
	for _, f := range solanatest.Fixtures {
		t.Run(f.File, func(t *testing.T) {
			c := serve(t, &node{
				method: "getTransaction",
				params: `["` + f.Signature + `", {"encoding": "base64", "commitment": "confirmed", "maxSupportedTransactionVersion": 1}]`,
				result: json.RawMessage(f.JSON(t)),
			})
			tx, err := c.Transaction(t.Context(), f.Signature)
			require.NoError(t, err)
			assert.Equal(t, f.Tx(t), tx)
		})
	}
}

func TestCheckDevnet(t *testing.T) {
	c := serve(t, &node{method: "getGenesisHash", result: solana.DevnetGenesisHash})
	require.NoError(t, c.CheckDevnet(t.Context()))

	c = serve(t, &node{method: "getGenesisHash", result: solanatest.Key("another cluster")})
	err := c.CheckDevnet(t.Context())
	require.ErrorIs(t, err, solana.ErrNotDevnet)
	assert.ErrorContains(t, err, solanatest.Key("another cluster"))
}

func rpcError(code int, message string) string {
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": code, "message": message}})
	if err != nil {
		panic(err)
	}
	return string(b)
}

var sentinels = []error{solana.ErrNotFound, solana.ErrRateLimited, solana.ErrPermanent, solana.ErrNotDevnet}

// requireClass checks that err is in want's class (nil for transient) and, when as is set, that
// errors.As still finds that type.
func requireClass(t *testing.T, err, want error, as any) {
	t.Helper()
	require.Error(t, err)
	for _, s := range sentinels {
		in := want != nil && errors.Is(want, s)
		assert.Equal(t, in, errors.Is(err, s), "errors.Is(err, %v) for %v", s, err)
	}
	if as != nil {
		assert.ErrorAs(t, err, as)
	}
}

func TestErrorClasses(t *testing.T) {
	tests := map[string]struct {
		node node
		want error // nil = transient: no sentinel applies
		as   any   // a type errors.As must still find
	}{
		"HTTP 429 with JSON-RPC code 429": {node: node{status: http.StatusTooManyRequests, body: rpcError(429, "Too many requests for a specific RPC call")},
			want: solana.ErrRateLimited, as: new(*jsonrpc.HTTPError)},
		"HTTP 429 with a provider's own code": {node: node{status: http.StatusTooManyRequests, body: rpcError(-32007, "15/second request limit reached")},
			want: solana.ErrRateLimited, as: new(*jsonrpc.HTTPError)},
		"HTTP 429 as plain text": {node: node{status: http.StatusTooManyRequests, body: "Too Many Requests"},
			want: solana.ErrRateLimited, as: new(*jsonrpc.HTTPError)},
		"JSON-RPC code 429 with HTTP 200": {node: node{status: http.StatusOK, body: rpcError(429, "Too many requests")},
			want: solana.ErrRateLimited, as: new(*jsonrpc.RPCError)},
		"HTTP 503 as HTML": {node: node{status: http.StatusServiceUnavailable, body: "<html><body>503 Service Unavailable</body></html>"},
			as: new(*jsonrpc.HTTPError)},
		"HTTP 503 with a JSON-RPC error": {node: node{status: http.StatusServiceUnavailable, body: rpcError(-32005, "Node is behind")},
			as: new(*jsonrpc.RPCError)},
		"another JSON-RPC error": {node: node{status: http.StatusOK, body: rpcError(-32005, "Node is unhealthy")},
			as: new(*jsonrpc.RPCError)},
		"unsupported transaction version": {node: node{status: http.StatusOK, body: rpcError(-32015, "Transaction version (2) is not supported")},
			want: solana.ErrPermanent, as: new(*jsonrpc.RPCError)},
		"a reply solana-go cannot parse": {node: node{status: http.StatusOK, body: "not json"}},
	}
	for name, tt := range tests {
		for call, do := range calls {
			t.Run(name+"/"+call, func(t *testing.T) {
				n := tt.node
				requireClass(t, do(t.Context(), serve(t, &n)), tt.want, tt.as)
			})
		}
	}
}

func TestTransactionErrorClasses(t *testing.T) {
	tests := map[string]struct {
		node node
		want error
	}{
		"not indexed yet":   {node: node{result: nil}, want: solana.ErrNotFound},
		"undecodable":       {node: node{result: map[string]any{"slot": 1, "transaction": []any{"AQID", "base64"}, "meta": map[string]any{}}}, want: solana.ErrPermanent},
		"another signature": {node: node{result: json.RawMessage(solanatest.V1.JSON(t))}, want: solana.ErrPermanent},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := serve(t, &tt.node).Transaction(t.Context(), solanatest.Devpay.Signature)
			requireClass(t, err, tt.want, nil)
		})
	}
}

func TestSignaturesErrorClasses(t *testing.T) {
	tests := map[string]struct {
		node node
		want error
	}{
		"a null entry":    {node: node{result: []any{nil}}, want: solana.ErrPermanent},
		"no result":       {node: node{status: http.StatusOK, body: `{"jsonrpc":"2.0","id":1}`}},
		"a null result":   {node: node{result: nil}},
		"a slot overflow": {node: node{result: []any{map[string]any{"signature": solanatest.Devpay.Signature, "slot": uint64(1) << 63}}}, want: solana.ErrPermanent},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := serve(t, &tt.node).Signatures(t.Context(), solanatest.Key("reference"), 100)
			requireClass(t, err, tt.want, nil)
		})
	}
}

func TestContextEndsPassThrough(t *testing.T) {
	tests := map[string]struct {
		node   node
		cancel bool // cancel after 50 ms; otherwise a 50 ms deadline
		want   error
	}{
		"deadline before the answer":    {node: node{delay: time.Second}, want: context.DeadlineExceeded},
		"deadline while reading a body": {node: node{stall: `{"jsonrpc":"2.0","id":1,"result":`}, want: context.DeadlineExceeded},
		"cancel while reading a body":   {node: node{stall: `{"jsonrpc":"2.0","id":1,"result":`}, cancel: true, want: context.Canceled},
	}
	for name, tt := range tests {
		for call, do := range calls {
			t.Run(name+"/"+call, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
				if tt.cancel {
					ctx, cancel = context.WithCancel(t.Context())
					time.AfterFunc(50*time.Millisecond, cancel)
				}
				defer cancel()
				n := tt.node
				err := do(ctx, serve(t, &n))
				require.ErrorIs(t, err, tt.want)
				requireClass(t, err, nil, nil)
			})
		}
	}
}

func TestRefusesBadArguments(t *testing.T) {
	c := serve(t, &node{method: "none"})
	_, err := c.Signatures(t.Context(), "not base58!", 100)
	require.ErrorIs(t, err, solana.ErrPermanent)
	_, err = c.Transaction(t.Context(), "not base58!")
	require.ErrorIs(t, err, solana.ErrPermanent)

	_, err = solana.New("http://user:pw-SECRET@%zz")
	require.EqualError(t, err, "solana: the RPC endpoint is not a valid URL")
}

func TestKeyedEndpointIsHidden(t *testing.T) {
	answers := map[string]node{
		"503":       {status: http.StatusServiceUnavailable, body: "upstream unavailable"},
		"plain 429": {status: http.StatusTooManyRequests, body: "slow down"},
		"JSON 429":  {status: http.StatusTooManyRequests, body: rpcError(-32007, "request limit reached")},
		// solana-go reports these two in its own error, which names the URL as well.
		"307 without a location":  {status: http.StatusTemporaryRedirect},
		"a reply it cannot parse": {status: http.StatusOK, body: "not json"},
	}
	// Each has the key in its credentials, path or query; the last prints differently once
	// parsed (lower-case scheme, an escaped space), which the client must hide as well.
	keyed := []string{
		"http://user:pw-SECRET@%s/v2/key-SECRET?api-key=SECRET\u200b",
		"HTTP://user:pw-SECRET@%s/v2/key SECRET/?api-key=SECRET",
	}
	for name, a := range answers {
		for call, do := range calls {
			for _, k := range keyed {
				t.Run(name+"/"+call, func(t *testing.T) {
					n := a
					n.t = t
					srv := httptest.NewServer(&n)
					t.Cleanup(srv.Close)
					host := strings.TrimPrefix(srv.URL, "http://")
					c, err := solana.New(strings.Replace(k, "%s", host, 1))
					require.NoError(t, err)

					err = do(t.Context(), c)
					require.Error(t, err)
					assert.NotContains(t, err.Error(), "SECRET")
					assert.Contains(t, err.Error(), "http://"+host)
				})
			}
		}
	}

	// Nothing listens on port 1 (nor, in CI, on port 80): net/http's own error quotes the URL.
	for _, endpoint := range []string{
		"http://user:pw-SECRET@127.0.0.1:1/v2/key-SECRET?api-key=SECRET",
		"http://127.0.0.1:1/?api-key=SECRET\u200b",
		`http://127.0.0.1:1/?api-key=SECRET"`,
		"HTTP://127.0.0.1:1/v2/key SECRET/",
		"http://user:pw-SECRET@127.0.0.1:/v2/key-SECRET?api-key=SECRET", // an empty port
	} {
		for call, do := range calls {
			c, err := solana.New(endpoint)
			require.NoError(t, err)
			err = do(t.Context(), c)
			require.Error(t, err, call)
			assert.NotContains(t, err.Error(), "SECRET", "%s %s", call, endpoint)
			assert.Contains(t, err.Error(), "http://127.0.0.1", "%s %s", call, endpoint)
		}
	}
}

func TestFollowsNoRedirect(t *testing.T) {
	for call, do := range calls {
		t.Run(call, func(t *testing.T) {
			var hits atomic.Int32
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":"`+solana.DevnetGenesisHash+`"}`)
			}))
			t.Cleanup(other.Close)
			named := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
			}))
			t.Cleanup(named.Close)
			c, err := solana.New(named.URL)
			require.NoError(t, err)

			require.Error(t, do(t.Context(), c))
			assert.Zero(t, hits.Load(), "no call reaches the node the redirect names")
		})
	}
}

func TestClientCallsShareTheLimiter(t *testing.T) {
	var mu sync.Mutex
	var arrived []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		arrived = append(arrived, time.Now())
		mu.Unlock()
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":[]}`)
	}))
	t.Cleanup(srv.Close)
	c, err := solana.New(srv.URL)
	require.NoError(t, err)

	for range 2 {
		_, err := c.Signatures(t.Context(), solanatest.Key("reference"), 100)
		require.NoError(t, err)
	}
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, arrived, 2)
	assert.GreaterOrEqual(t, arrived[1].Sub(arrived[0]), 1200*time.Millisecond, "the second call waits for the limiter")
}
