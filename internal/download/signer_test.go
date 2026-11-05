package download_test

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/download"
)

var (
	orderID = uuid.MustParse("3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e")
	// madeAt is when the tests make their links, which expire 15 minutes later.
	madeAt = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	expiry = madeAt.Add(15 * time.Minute)
)

// clock is a clock that a test sets.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// newSecret returns a random secret, for one test.
func newSecret() []byte {
	return []byte(rand.Text() + rand.Text())
}

// split returns the order id in a link's path and the exp and sig of its query.
func split(t *testing.T, link string) (id, exp, sig string) {
	t.Helper()
	u, err := url.Parse(link)
	require.NoError(t, err)
	id, ok := strings.CutPrefix(u.Path, "/d/")
	require.True(t, ok, link)
	return id, u.Query().Get("exp"), u.Query().Get("sig")
}

// flip returns sig with the base64url character at i replaced by the one that differs from it in
// the lowest of its six bits.
func flip(sig string, i int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	b := []byte(sig)
	b[i] = alphabet[strings.IndexByte(alphabet, b[i])^1]
	return string(b)
}

// A link is /d/{id}?exp=…&sig=…, with the expiry in Unix seconds and the HMAC-SHA256 of
// "{id}|{exp}" in unpadded base64url.
func TestSignerPath(t *testing.T) {
	secret := newSecret()
	c := &clock{madeAt.Add(900 * time.Millisecond)} // the expiry rounds down to the second
	for _, ttl := range []time.Duration{15 * time.Minute, time.Hour} {
		link := download.NewSigner(secret, ttl, c.now).Path(orderID)

		exp := strconv.FormatInt(madeAt.Add(ttl).Unix(), 10)
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte(orderID.String() + "|" + exp))
		sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		assert.Len(t, sig, 43)
		assert.Equal(t, "/d/3c9e6f1a-2b4d-4e8f-a1c3-5d7e9f0b2c4e?exp="+exp+"&sig="+sig, link, ttl)
	}
}

func TestSignerChecksTheExpiry(t *testing.T) {
	c := &clock{madeAt}
	s := download.NewSigner(newSecret(), 15*time.Minute, c.now)
	_, exp, sig := split(t, s.Path(orderID))
	for _, at := range []time.Time{madeAt, expiry} {
		c.t = at
		require.NoError(t, s.Check(orderID, exp, sig), "at %s", at)
	}
	c.t = expiry.Add(time.Nanosecond)
	assert.ErrorIs(t, s.Check(orderID, exp, sig), download.ErrLinkExpired)
}

// A link that Signer did not make is refused, before and after its expiry, so a forged link
// learns nothing about whether its time is up.
func TestSignerRefusesALinkItDidNotMake(t *testing.T) {
	c := &clock{madeAt}
	s := download.NewSigner(newSecret(), 15*time.Minute, c.now)
	_, exp, sig := split(t, s.Path(orderID))
	_, _, otherSig := split(t, download.NewSigner(newSecret(), 15*time.Minute, c.now).Path(orderID))

	// Decoding drops the two lowest bits of the last character, and skips line breaks.
	want, err := base64.RawURLEncoding.DecodeString(sig)
	require.NoError(t, err)
	for _, changed := range []string{flip(sig, 42), sig[:10] + "\n" + sig[10:]} {
		got, err := base64.RawURLEncoding.DecodeString(changed)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}

	tests := []struct {
		name     string
		id       uuid.UUID
		exp, sig string
	}{
		{"another order", uuid.MustParse("6ba7b810-9dad-41d1-80b4-00c04fd430c8"), exp, sig},
		{"a later expiry", orderID, strconv.FormatInt(expiry.Add(time.Hour).Unix(), 10), sig},
		{"another secret", orderID, exp, otherSig},
		{"a changed character", orderID, exp, flip(sig, 10)},
		{"a changed bit that decoding drops", orderID, exp, flip(sig, 42)},
		{"a line break", orderID, exp, sig[:10] + "\n" + sig[10:]},
		{"padding", orderID, exp, sig + "="},
		{"a short signature", orderID, exp, sig[:42]},
		{"no signature", orderID, exp, ""},
		{"an expiry with a sign", orderID, "+" + exp, sig},
		{"an expiry with a leading zero", orderID, "0" + exp, sig},
		{"an expiry that is not a number", orderID, exp + "x", sig},
		{"no expiry", orderID, "", sig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, at := range []time.Time{madeAt, expiry.Add(time.Hour)} {
				c.t = at
				assert.ErrorIs(t, s.Check(tt.id, tt.exp, tt.sig), download.ErrBadSignature, "at %s", at)
			}
		})
	}
}
