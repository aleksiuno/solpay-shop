// Package config loads the service settings from the environment and validates them.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mr-tron/base58"
)

// Config holds every setting the service reads from the environment.
type Config struct {
	Addr            string        // APP_ADDR
	DatabaseURL     string        // DATABASE_URL
	SolanaRPCURL    string        // SOLANA_RPC_URL; may embed an API key, so never log it
	MerchantAddress string        // MERCHANT_ADDRESS, the base58 public key that receives payments
	OrderTTL        time.Duration // ORDER_TTL
	PollInterval    time.Duration // POLL_INTERVAL
	AdminUser       string        // ADMIN_USER
	AdminPass       string        // ADMIN_PASS
	LogFile         string        // LOG_FILE; empty means stdout only
	DownloadSecret  []byte        // DOWNLOAD_SECRET, the HMAC key for download links
	FilesDir        string        // FILES_DIR
}

// FieldError reports one invalid setting. It never contains the value, which may be a secret.
type FieldError struct {
	Key     string
	Problem string
}

func (e *FieldError) Error() string { return e.Key + ": " + e.Problem }

const (
	minAdminPass      = 12
	minDownloadSecret = 32
	publicKeySize     = 32
)

// Load reads the settings through getenv, normally os.Getenv. It reports every invalid setting
// at once, joined with errors.Join, so a bad configuration is fixed in one pass.
//
// DATABASE_URL, MERCHANT_ADDRESS, ADMIN_USER, ADMIN_PASS and DOWNLOAD_SECRET have no default:
// a built-in credential would be known to anyone who reads this code, and a built-in merchant
// address would send payments to a wallet the operator does not control.
func Load(getenv func(string) string) (Config, error) {
	l := loader{getenv: getenv}
	c := Config{
		Addr:            l.addr("APP_ADDR", ":8080"),
		DatabaseURL:     l.databaseURL("DATABASE_URL"),
		SolanaRPCURL:    l.rpcURL("SOLANA_RPC_URL", "https://api.devnet.solana.com"),
		MerchantAddress: l.publicKey("MERCHANT_ADDRESS"),
		OrderTTL:        l.duration("ORDER_TTL", 15*time.Minute, time.Minute, 24*time.Hour),
		PollInterval:    l.duration("POLL_INTERVAL", 5*time.Second, time.Second, time.Minute),
		AdminUser:       l.adminUser("ADMIN_USER"),
		AdminPass:       l.secret("ADMIN_PASS", minAdminPass, "characters", utf8.RuneCountInString),
		LogFile:         l.optional("LOG_FILE", ""),
		DownloadSecret:  []byte(l.secret("DOWNLOAD_SECRET", minDownloadSecret, "bytes", lenBytes)),
		FilesDir:        l.optional("FILES_DIR", "./testdata/files"),
	}
	if len(l.errs) > 0 {
		return Config{}, errors.Join(l.errs...)
	}
	return c, nil
}

// loader collects one error per invalid setting.
type loader struct {
	getenv func(string) string
	errs   []error
}

func (l *loader) fail(key, problem string) {
	l.errs = append(l.errs, &FieldError{Key: key, Problem: problem})
}

func (l *loader) optional(key, def string) string {
	if v := l.getenv(key); v != "" {
		return v
	}
	return def
}

func (l *loader) required(key string) string {
	v := l.getenv(key)
	if v == "" {
		l.fail(key, "required")
	}
	return v
}

func (l *loader) addr(key, def string) string {
	v := l.optional(key, def)
	_, port, err := net.SplitHostPort(v)
	if err == nil {
		_, err = strconv.ParseUint(port, 10, 16)
	}
	if err != nil {
		l.fail(key, "must be host:port or :port with a numeric port")
	}
	return v
}

func (l *loader) databaseURL(key string) string {
	v := l.required(key)
	if v == "" {
		return v
	}
	u, err := url.Parse(v)
	if err != nil || u.Hostname() == "" || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		l.fail(key, "must be a postgres:// URL with a host")
	}
	return v
}

// rpcURL allows plain http only to a local validator: the watcher marks orders paid on the RPC
// node's word, and a keyed URL would send its key in clear text.
func (l *loader) rpcURL(key, def string) string {
	v := l.optional(key, def)
	u, err := url.Parse(v)
	switch {
	case err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http"):
		l.fail(key, "must be an https URL with a host")
	case u.Scheme == "http" && !isLoopback(u.Hostname()):
		l.fail(key, "may use plain http only for localhost or a loopback address")
	}
	return v
}

func (l *loader) publicKey(key string) string {
	v := l.required(key)
	if v == "" {
		return v
	}
	b, err := base58.Decode(v)
	switch {
	case err != nil:
		l.fail(key, "not a base58 address")
	case len(b) != publicKeySize:
		l.fail(key, fmt.Sprintf("decodes to %d bytes; a Solana address has %d", len(b), publicKeySize))
	}
	return v
}

func (l *loader) duration(key string, def, lo, hi time.Duration) time.Duration {
	v := l.getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	switch {
	case err != nil:
		l.fail(key, "not a Go duration such as 15m or 5s")
	case d < lo || d > hi:
		l.fail(key, fmt.Sprintf("must be between %s and %s", shortDuration(lo), shortDuration(hi)))
	}
	return d
}

func (l *loader) adminUser(key string) string {
	v := l.required(key)
	if strings.Contains(v, ":") {
		l.fail(key, "must not contain ':', which basic auth uses as a separator")
	}
	return v
}

func (l *loader) secret(key string, minLen int, unit string, length func(string) int) string {
	v := l.required(key)
	if v != "" && length(v) < minLen {
		l.fail(key, fmt.Sprintf("must be at least %d %s", minLen, unit))
	}
	return v
}

func lenBytes(s string) int { return len(s) }

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// shortDuration drops zero trailing units, so a bound reads 24h rather than 24h0m0s.
func shortDuration(d time.Duration) string {
	s := d.String()
	if t, ok := strings.CutSuffix(s, "m0s"); ok {
		s = t + "m"
	}
	if t, ok := strings.CutSuffix(s, "h0m"); ok {
		s = t + "h"
	}
	return s
}
