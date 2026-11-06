package solana

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// DevnetRPCURL is Solana's public devnet endpoint, which needs no key.
const DevnetRPCURL = "https://api.devnet.solana.com"

var errBadEndpoint = errors.New("solana: the RPC endpoint must be an https URL, or http to a loopback host")

// Endpoint is a checked JSON-RPC URL: https, or plain http only to a loopback host, such as a
// local proxy to devnet. Payments are trusted on the node's word, and a keyed URL would send its
// key in clear text. A keyed endpoint carries its API key in the URL's path, query or userinfo,
// so an Endpoint names itself only as scheme://host. Use ParseEndpoint to make one.
type Endpoint struct {
	// No field holds the URL itself: fmt prints an Endpoint inside another struct's unexported
	// field one field at a time, without calling String, and prints the replacer as an address.
	safe string
	hide *strings.Replacer
}

// ParseEndpoint checks raw. The error never repeats it.
func ParseEndpoint(raw string) (Endpoint, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.Scheme != "https" && (u.Scheme != "http" || !isLoopback(u.Hostname())) {
		return Endpoint{}, errBadEndpoint
	}
	// solana-go prints the URL of the request it sent, which net/http normalizes (an empty port
	// is dropped, for example), so the replacer also knows that form.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, raw, nil)
	if err != nil {
		return Endpoint{}, errBadEndpoint
	}
	safe := u.Scheme + "://" + u.Host
	return Endpoint{safe: safe, hide: strings.NewReplacer(raw, safe, u.String(), safe, req.URL.String(), safe)}, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// String returns scheme://host, so an Endpoint printed by mistake never shows its key.
func (e Endpoint) String() string { return e.safe }

// Redact replaces the endpoint's URL in s with scheme://host, in each form that solana-go may
// print it.
func (e Endpoint) Redact(s string) string { return e.hide.Replace(s) }

// HTTPClient returns a client for the endpoint whose requests time out after timeout. It is the
// HTTP client to give solana-go's jsonrpc package.
func (e Endpoint) HTTPClient(timeout time.Duration) *EndpointClient {
	return &EndpointClient{
		hc: &http.Client{
			Timeout: timeout,
			// The devnet check vouches for this endpoint only, so a redirect must not lead
			// elsewhere, such as to a node on another cluster.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		safe: e.safe,
	}
}

// EndpointClient is the HTTP client of one Endpoint. It follows no redirect, and its errors give
// the endpoint as scheme://host: net/http quotes the URL in its errors, so a replacer could miss
// it there.
type EndpointClient struct {
	hc   *http.Client
	safe string
}

// Do sends req and returns the answer, as http.Client.Do does.
func (c *EndpointClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.hc.Do(req) //nolint:gosec // G704: the URL is the endpoint that ParseEndpoint checked
	if ue, ok := errors.AsType[*url.Error](err); ok {
		ue.URL = c.safe
	}
	return resp, err
}

// CloseIdleConnections closes the connections that the client keeps open for reuse.
func (c *EndpointClient) CloseIdleConnections() { c.hc.CloseIdleConnections() }
