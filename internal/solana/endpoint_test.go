package solana_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go/rpc/jsonrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/solana"
)

// solana-go takes the client through this interface; the package's own API names no SDK type.
var _ jsonrpc.HTTPClient = (*solana.EndpointClient)(nil)

func TestParseEndpoint(t *testing.T) {
	for _, ok := range []string{solana.DevnetRPCURL, "http://localhost:8899", "http://127.0.0.1:8899", "http://127.0.0.2:8899", "http://[::1]:8899"} {
		_, err := solana.ParseEndpoint(ok)
		require.NoError(t, err, ok)
	}
	// Plain http to another host would send a key in clear text, an IP address included, and a
	// URL with a port but no host would dial this machine.
	for _, bad := range []string{
		"http://rpc.example/key-SECRET", "http://localhost.example", "http://192.0.2.1:8899/key-SECRET",
		"http://0.0.0.0:8899", "http://[::]:8899", "ftp://rpc.example", "rpc.example",
		"https://", "https://:443", "http://:8899", "http://user:pw-SECRET@%zz",
	} {
		_, err := solana.ParseEndpoint(bad)
		require.EqualError(t, err, "solana: the RPC endpoint must be an https URL, or http to a loopback host", bad)
	}
}

func TestEndpointRedacts(t *testing.T) {
	// Each has the key in its credentials, path or query. The last two print differently once
	// parsed or sent: an upper-case scheme and an escaped space, and an empty port that net/http
	// drops from the request.
	tests := []struct {
		raw     string
		printed []string // other forms in which solana-go may print the URL
		safe    string
	}{
		{"https://user:pw-SECRET@rpc.example/v2/key-SECRET?api-key=SECRET", nil, "https://rpc.example"},
		{"HTTP://localhost:8899/v2/key SECRET/", []string{"http://localhost:8899/v2/key%20SECRET/"}, "http://localhost:8899"},
		{"http://user:pw-SECRET@127.0.0.1:/v2/key-SECRET", []string{"http://user:pw-SECRET@127.0.0.1/v2/key-SECRET"}, "http://127.0.0.1:"},
	}
	for _, tt := range tests {
		e, err := solana.ParseEndpoint(tt.raw)
		require.NoError(t, err, tt.raw)
		assert.Equal(t, tt.safe+" "+tt.safe, fmt.Sprintf("%v %+v", e, e), "printing an Endpoint shows scheme://host only")
		assert.NotContains(t, fmt.Sprintf("%#v", e), "SECRET", "nor do its fields")
		for _, form := range append([]string{tt.raw}, tt.printed...) {
			assert.Equal(t, "rpc call getBalance() on "+tt.safe+": EOF", e.Redact("rpc call getBalance() on "+form+": EOF"), form)
		}
	}
}

func TestEndpointClientNamesSchemeAndHostOnly(t *testing.T) {
	// Nothing listens on port 1, so the request fails with net/http's own error, which quotes the
	// URL and so escapes an invisible character or a quote that a replacer would look for.
	for _, raw := range []string{
		"http://user:pw-SECRET@127.0.0.1:1/v2/key-SECRET?api-key=SECRET",
		"http://127.0.0.1:1/?api-key=SECRET\u200b",
		`http://127.0.0.1:1/?api-key=SECRET"`,
	} {
		e, err := solana.ParseEndpoint(raw)
		require.NoError(t, err, raw)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, raw, http.NoBody)
		require.NoError(t, err, raw)
		resp, err := e.HTTPClient(time.Second).Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		require.Error(t, err, raw)
		assert.NotContains(t, err.Error(), "SECRET", raw)
		assert.Contains(t, err.Error(), `"http://127.0.0.1:1"`, raw)
	}
}

func TestEndpointClientTimesOut(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(time.Second):
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)
	keyed := strings.Replace(slow.URL, "http://", "http://user:pw-SECRET@", 1) + "/v2/key-SECRET?api-key=SECRET"

	e, err := solana.ParseEndpoint(keyed)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, keyed, http.NoBody)
	require.NoError(t, err)
	resp, err := e.HTTPClient(50 * time.Millisecond).Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.ErrorContains(t, err, "Client.Timeout exceeded", "the node answers only after a second")
	assert.NotContains(t, err.Error(), "SECRET")
	assert.Contains(t, err.Error(), `"`+slow.URL+`"`)
}

func TestEndpointClientFollowsNoRedirect(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(other.Close)
	named := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(named.Close)

	e, err := solana.ParseEndpoint(named.URL)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, named.URL, http.NoBody)
	require.NoError(t, err)
	resp, err := e.HTTPClient(time.Second).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode, "the redirect is the answer")
	assert.Zero(t, hits.Load(), "no request reaches the address the redirect names")
}
