package download

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"time"
	"uuid"
)

var (
	// ErrBadSignature: the link was not made by the shop, or was changed after.
	ErrBadSignature = errors.New("download: bad signature")
	// ErrLinkExpired: the shop made the link, but its time is up.
	ErrLinkExpired = errors.New("download: link expired")
)

// Signer makes and checks download links. A link names an order and the time it expires, and
// carries an HMAC-SHA256 of both under a secret that only the shop knows, so no one else can make
// a link, point one at another order or give one more time.
type Signer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewSigner returns a Signer whose links are signed with secret and expire ttl after they are
// made, by the clock now.
func NewSigner(secret []byte, ttl time.Duration, now func() time.Time) Signer {
	return Signer{secret: secret, ttl: ttl, now: now}
}

// Path returns the path of a new download link for the order with id:
// /d/{id}?exp={expiry}&sig={signature}. The expiry is in Unix seconds, rounded down, and the
// signature is the HMAC-SHA256 of the id and the expiry joined by "|", in unpadded base64url.
func (s Signer) Path(id uuid.UUID) string {
	exp := strconv.FormatInt(s.now().Add(s.ttl).Unix(), 10)
	return "/d/" + id.String() + "?" + url.Values{"exp": {exp}, "sig": {s.sign(id, exp)}}.Encode()
}

// Check reports whether exp and sig, from a link to the order with id, came from Path and the link
// still works. It returns ErrBadSignature for a link that Path did not make, and ErrLinkExpired for
// one past its expiry. The signature is checked first, so a forged link learns nothing about
// expiry.
//
// The signature covers the expiry as written, and is compared as text with the one Path writes,
// so only the spelling Path uses passes: not an expiry with a sign or a leading zero, nor a
// signature with a line break or other bits in its last character, which base64 decoding would
// accept.
func (s Signer) Check(id uuid.UUID, exp, sig string) error {
	t, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return ErrBadSignature
	}
	if !hmac.Equal([]byte(sig), []byte(s.sign(id, exp))) {
		return ErrBadSignature
	}
	if s.now().After(time.Unix(t, 0)) {
		return ErrLinkExpired
	}
	return nil
}

// sign returns the signature of a link to the order with id that expires at exp.
func (s Signer) sign(id uuid.UUID, exp string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(id.String() + "|" + exp))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
